package artistbio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/logger"
)

type webSearcher interface {
	Search(ctx context.Context, input ArtistSearchQuery) (WebSearchReport, error)
}

type identityResolverAPI interface {
	Resolve(ctx context.Context, input IdentityContext) (IdentityResolution, error)
}

type ingestionAPI interface {
	Ingest(ctx context.Context, artist ResolvedArtist) (IngestionReport, error)
}

type validationAPI interface {
	ValidateArtist(ctx context.Context, artistID string) (ValidationReport, error)
}

type synthesisAI interface {
	Collect(ctx context.Context, identityJSON json.RawMessage, results []ai.SearchEvidenceInput) ([]ai.CollectedEvidence, error)
	Edit(ctx context.Context, evidence []ai.SynthesisEvidence) (ai.BiographySummary, error)
	Verify(ctx context.Context, summary ai.BiographySummary, evidence []ai.SynthesisEvidence) (ai.SynthesisVerification, error)
}

type SynthesisPipeline struct {
	store     *Store
	local     LocalIdentityContextSource
	resolver  identityResolverAPI
	ingestion ingestionAPI
	validator validationAPI
	search    webSearcher
	ai        synthesisAI
}

func NewSynthesisPipeline(store *Store, local LocalIdentityContextSource, resolver identityResolverAPI, ingestion ingestionAPI, validator validationAPI, search webSearcher, model synthesisAI) (*SynthesisPipeline, error) {
	if store == nil || resolver == nil || ingestion == nil || validator == nil || model == nil {
		return nil, fmt.Errorf("biography synthesis pipeline requires store, resolver, ingestion, validator, and AI")
	}
	return &SynthesisPipeline{store: store, local: local, resolver: resolver, ingestion: ingestion, validator: validator, search: search, ai: model}, nil
}

func (p *SynthesisPipeline) Run(ctx context.Context, artist db.LibraryArtist) (err error) {
	started := time.Now()
	if err := p.store.MarkJobRunning(ctx, artist.ID, started); err != nil {
		return err
	}
	defer func() {
		logger.Debug("[BIO] artist_id=%q pipeline_duration=%s outcome=%s", artist.ID, time.Since(started).Round(time.Millisecond), pipelineOutcome(err))
	}()

	identityContext := p.localContext(ctx, artist)
	stageStarted := time.Now()
	resolution, err := p.resolver.Resolve(ctx, identityContext)
	logPipelineStage(artist.ID, "identity", stageStarted, err)
	if err != nil {
		return p.retryable(ctx, artist.ID, "identity_unavailable", err)
	}
	if resolution.Status != ResolutionResolved {
		return p.store.CompleteNoData(ctx, artist.ID, "identity-unresolved", time.Now())
	}
	identityContext.MusicBrainzID = resolution.MusicBrainzID
	stageStarted = time.Now()
	if _, err := p.ingestion.Ingest(ctx, ResolvedArtist{Context: identityContext, Identity: resolution}); err != nil {
		logPipelineStage(artist.ID, "catalog", stageStarted, err)
		return p.retryable(ctx, artist.ID, "catalog_unavailable", err)
	}
	logPipelineStage(artist.ID, "catalog", stageStarted, nil)
	stageStarted = time.Now()
	if _, err := p.validator.ValidateArtist(ctx, artist.ID); err != nil {
		logPipelineStage(artist.ID, "source_validation", stageStarted, err)
		return p.retryable(ctx, artist.ID, "validation_unavailable", err)
	}
	logPipelineStage(artist.ID, "source_validation", stageStarted, nil)

	stageStarted = time.Now()
	prepared, err := p.store.PrepareCatalogEvidence(ctx, artist.ID)
	logPipelineStage(artist.ID, "evidence", stageStarted, err)
	if err != nil {
		return p.retryable(ctx, artist.ID, "evidence_unavailable", err)
	}
	evidence := catalogSynthesisEvidence(prepared.Items)
	var discoveryErr error
	if !prepared.Sufficient && p.search != nil {
		stageStarted = time.Now()
		if web, webErr := p.collectWebEvidence(ctx, identityContext, resolution); len(web) > 0 {
			evidence = append(evidence, web...)
		} else {
			discoveryErr = webErr
		}
		logPipelineStage(artist.ID, "web_discovery", stageStarted, discoveryErr)
	}
	evidence = boundSynthesisEvidence(evidence, maxSynthesisEvidenceItems)
	version := prepared.Version
	if computed, versionErr := synthesisEvidenceVersion(prepared.Version, evidence); versionErr == nil {
		version = computed
	}

	if countEvidenceOrigins(evidence) >= 2 {
		stageStarted = time.Now()
		summary, editErr := p.ai.Edit(ctx, evidence)
		logPipelineStage(artist.ID, "summary_edit", stageStarted, editErr)
		if editErr == nil {
			stageStarted = time.Now()
			verification, verifyErr := p.ai.Verify(ctx, summary, evidence)
			logPipelineStage(artist.ID, "summary_verify", stageStarted, verifyErr)
			if verifyErr == nil && verification.Grounded {
				finalText := summary.Summary + "\n\n" + synthesisDisclosure(summary.UsedSourceIDs, evidence)
				proof, _ := json.Marshal(struct {
					Version   string                `json:"version"`
					Sources   []string              `json:"sources"`
					Sentences []ai.SummarySentence  `json:"sentences"`
					Evidence  []storedSummarySource `json:"evidence"`
				}{version, summary.UsedSourceIDs, summary.Sentences, storedSummarySources(summary.UsedSourceIDs, evidence)})
				_, err = p.store.CommitAISummary(ctx, artist.ID, version, finalText, string(proof), time.Now())
				return err
			}
		}
	}
	if _, found, readErr := p.store.AutomaticCandidate(ctx, artist.ID); readErr != nil {
		return p.retryable(ctx, artist.ID, "selection_unavailable", readErr)
	} else if found {
		return p.store.CompleteReady(ctx, artist.ID, version, time.Now())
	}
	if discoveryErr != nil {
		return p.retryable(ctx, artist.ID, "search_unavailable", discoveryErr)
	}
	if ctx.Err() != nil {
		return p.retryable(context.Background(), artist.ID, "pipeline_timeout", ctx.Err())
	}
	return p.store.CompleteNoData(ctx, artist.ID, version, time.Now())
}

const maxSynthesisEvidenceItems = 8

func (p *SynthesisPipeline) localContext(ctx context.Context, artist db.LibraryArtist) IdentityContext {
	result := IdentityContext{ArtistID: artist.ID, Name: artist.Name}
	if p.local == nil {
		return result
	}
	local, err := p.local.ArtistIdentityContext(ctx, artist.Name, MaxIdentityAlbums, MaxIdentityTracks)
	if err != nil {
		return result
	}
	for _, album := range local.Albums {
		result.Albums = append(result.Albums, IdentityRelease{Title: album.Title, Year: album.Year})
	}
	result.Tracks = append(result.Tracks, local.Tracks...)
	bounded, err := BoundIdentityContext(result)
	if err != nil {
		return IdentityContext{ArtistID: artist.ID, Name: artist.Name}
	}
	return bounded
}

func (p *SynthesisPipeline) collectWebEvidence(ctx context.Context, identity IdentityContext, resolution IdentityResolution) ([]ai.SynthesisEvidence, error) {
	albums := make([]string, 0, min(3, len(identity.Albums)))
	for _, album := range identity.Albums[:min(3, len(identity.Albums))] {
		albums = append(albums, album.Title)
	}
	searchInput := ArtistSearchQuery{
		ArtistID: identity.ArtistID, Name: identity.Name, MusicBrainzID: resolution.MusicBrainzID,
		ProviderIDs: []string{string(resolution.Provider) + ":" + resolution.ProviderArtistID},
		Albums:      albums, Tracks: identity.Tracks[:min(5, len(identity.Tracks))],
	}
	_, queryVersion, err := buildArtistSearchQuery(searchInput)
	if err != nil {
		return nil, err
	}
	report, found, err := p.store.WebSearchCache(ctx, identity.ArtistID, queryVersion, time.Now())
	if err != nil {
		return nil, err
	}
	if !found {
		report, err = p.search.Search(ctx, searchInput)
		if err == nil {
			ttl := 90 * 24 * time.Hour
			if len(report.Results) == 0 {
				ttl = 30 * 24 * time.Hour
			}
			err = p.store.PutWebSearchCache(ctx, identity.ArtistID, queryVersion, report.Results, time.Now().Add(ttl))
		}
	}
	if err != nil || len(report.Results) == 0 {
		return nil, err
	}
	identityJSON, _ := json.Marshal(struct {
		Name          string `json:"name"`
		MusicBrainzID string `json:"musicbrainzId,omitempty"`
		Provider      string `json:"provider"`
		ProviderID    string `json:"providerId"`
	}{identity.Name, resolution.MusicBrainzID, string(resolution.Provider), resolution.ProviderArtistID})
	inputs := make([]ai.SearchEvidenceInput, 0, len(report.Results))
	byID := make(map[string]WebSearchResult, len(report.Results))
	for _, result := range report.Results {
		inputs = append(inputs, ai.SearchEvidenceInput{ID: result.ID, Title: result.Title, URL: result.URL, Domain: result.Domain, Snippet: result.Snippet})
		byID[result.ID] = result
	}
	cards, err := p.ai.Collect(ctx, identityJSON, inputs)
	if err != nil {
		return nil, err
	}
	output := make([]ai.SynthesisEvidence, 0, len(cards))
	for _, card := range cards {
		result, ok := byID[card.ResultID]
		if !ok {
			continue
		}
		output = append(output, ai.SynthesisEvidence{ID: "web:" + card.ID, Origin: result.Domain, URL: result.URL, Language: card.Language, Text: card.Fact})
	}
	return output, nil
}

func (p *SynthesisPipeline) retryable(ctx context.Context, artistID, code string, cause error) error {
	updateCtx := ctx
	if ctx.Err() != nil {
		updateCtx = context.Background()
	}
	if stateErr := p.store.MarkJobRetryable(updateCtx, artistID, code, time.Now().Add(time.Hour), time.Now()); stateErr != nil {
		return stateErr
	}
	return cause
}

func catalogSynthesisEvidence(items []EvidenceItem) []ai.SynthesisEvidence {
	result := make([]ai.SynthesisEvidence, 0, len(items))
	for _, item := range items {
		result = append(result, ai.SynthesisEvidence{ID: item.ID, Origin: item.Origin, URL: item.SourceURL, Language: item.Language, Text: item.Text})
	}
	return result
}

func boundSynthesisEvidence(input []ai.SynthesisEvidence, limit int) []ai.SynthesisEvidence {
	result := make([]ai.SynthesisEvidence, 0, min(limit, len(input)))
	remaining := 5 << 10
	for _, item := range input {
		if len(result) >= limit || remaining <= 0 {
			break
		}
		item.Text = truncateUTF8Bytes(item.Text, remaining)
		if len(item.Text) < minBiographyLetters {
			continue
		}
		remaining -= len(item.Text)
		result = append(result, item)
	}
	return result
}

type storedSummarySource struct {
	ID     string `json:"id"`
	Origin string `json:"origin"`
	URL    string `json:"url,omitempty"`
}

func storedSummarySources(used []string, evidence []ai.SynthesisEvidence) []storedSummarySource {
	wanted := make(map[string]struct{}, len(used))
	for _, id := range used {
		wanted[id] = struct{}{}
	}
	result := make([]storedSummarySource, 0, len(wanted))
	for _, item := range evidence {
		if _, ok := wanted[item.ID]; ok {
			result = append(result, storedSummarySource{ID: item.ID, Origin: item.Origin, URL: item.URL})
		}
	}
	return result
}

func synthesisEvidenceVersion(catalogVersion string, evidence []ai.SynthesisEvidence) (string, error) {
	items := make([]EvidenceItem, 0, len(evidence)+1)
	items = append(items, EvidenceItem{ID: "catalog-version", Origin: catalogVersion})
	for index, item := range evidence {
		items = append(items, EvidenceItem{
			ID: item.ID, Origin: item.Origin, SourceURL: item.URL, Language: item.Language,
			Text: item.Text, UpdatedUnix: int64(index),
		})
	}
	return evidenceVersion(items)
}

func countEvidenceOrigins(items []ai.SynthesisEvidence) int {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[item.Origin] = struct{}{}
	}
	return len(set)
}

func synthesisDisclosure(used []string, evidence []ai.SynthesisEvidence) string {
	byID := make(map[string]string, len(evidence))
	for _, item := range evidence {
		byID[item.ID] = displayEvidenceOrigin(item.Origin)
	}
	origins := make(map[string]struct{})
	for _, id := range used {
		if origin := byID[id]; origin != "" {
			origins[origin] = struct{}{}
		}
	}
	names := make([]string, 0, len(origins))
	for origin := range origins {
		names = append(names, origin)
	}
	sort.Strings(names)
	return "Саммари создано ИИ на основе проверенных источников: " + strings.Join(names, ", ") + "."
}

func displayEvidenceOrigin(value string) string {
	switch Provider(value) {
	case ProviderYandexMusic:
		return "Яндекс Музыка"
	case ProviderWikipedia:
		return "Wikipedia"
	case ProviderAppleMusic:
		return "Apple Music"
	case ProviderDeezer:
		return "Deezer"
	case ProviderTheAudioDB:
		return "TheAudioDB"
	case ProviderLastFM:
		return "Last.fm"
	case ProviderDiscogs:
		return "Discogs"
	case ProviderOfficial:
		return "официальный сайт"
	default:
		return value
	}
}

func pipelineOutcome(err error) string {
	if err == nil {
		return "complete"
	}
	return "failed"
}

func logPipelineStage(artistID, stage string, started time.Time, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	if err == nil {
		logger.Debug("[BIO] artist_id=%q stage=%s duration=%s outcome=%s", artistID, stage, time.Since(started).Round(time.Millisecond), outcome)
		return
	}
	logger.Debug("[BIO] artist_id=%q stage=%s duration=%s outcome=%s reason=%s", artistID, stage, time.Since(started).Round(time.Millisecond), outcome, safePipelineErrorReason(err))
}

func safePipelineErrorReason(err error) string {
	var safe interface{ SafeReason() string }
	if errors.As(err, &safe) && safe.SafeReason() != "" {
		return safe.SafeReason()
	}
	return "internal_error"
}

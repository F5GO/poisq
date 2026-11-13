package artistbio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maxBiographySources          = 16
	maxBiographyEvidenceCodes    = 32
	maxBiographyEvidenceCodeSize = 128
	maxSourcesPerArtist          = 4
)

// ResolvedArtist combines bounded library context with a decisive real-catalog identity.
type ResolvedArtist struct {
	Context  IdentityContext
	Identity IdentityResolution
}

// BiographySourceResult is attributable prose returned by one real provider.
type BiographySourceResult struct {
	SourceArtistID  string
	SourceURL       string
	OriginalText    string
	LanguageHint    string
	MatchConfidence float64
	EvidenceCodes   []string
}

// BiographySource fetches one provider biography for an already resolved artist.
type BiographySource interface {
	Provider() Provider
	Fetch(ctx context.Context, artist ResolvedArtist) (*BiographySourceResult, error)
}

type IngestionReport struct {
	Stored int
	Empty  int
	Failed int
}

type IngestionService struct {
	store   *Store
	sources []BiographySource
	global  chan struct{}

	mu       sync.Mutex
	inflight map[string]*ingestionCall
}

type ingestionCall struct {
	done   chan struct{}
	report IngestionReport
	err    error
}

type sourceFetchResult struct {
	provider Provider
	result   *BiographySourceResult
	err      error
}

func NewIngestionService(store *Store, sources []BiographySource, globalConcurrency int) (*IngestionService, error) {
	if store == nil {
		return nil, fmt.Errorf("biography ingestion requires store")
	}
	if len(sources) == 0 || len(sources) > maxBiographySources {
		return nil, fmt.Errorf("biography ingestion requires 1 to %d sources", maxBiographySources)
	}
	seen := make(map[Provider]struct{}, len(sources))
	copySources := make([]BiographySource, 0, len(sources))
	for _, source := range sources {
		if source == nil {
			return nil, fmt.Errorf("biography ingestion source is nil")
		}
		provider := source.Provider()
		if !validBiographyProvider(provider) {
			return nil, fmt.Errorf("invalid biography source provider %q", provider)
		}
		if _, exists := seen[provider]; exists {
			return nil, fmt.Errorf("duplicate biography source provider %q", provider)
		}
		seen[provider] = struct{}{}
		copySources = append(copySources, source)
	}
	if globalConcurrency < 1 {
		globalConcurrency = maxSourcesPerArtist
	}
	if globalConcurrency > maxBiographySources {
		globalConcurrency = maxBiographySources
	}
	return &IngestionService{
		store: store, sources: copySources, global: make(chan struct{}, globalConcurrency),
		inflight: make(map[string]*ingestionCall),
	}, nil
}

func (s *IngestionService) Ingest(ctx context.Context, artist ResolvedArtist) (IngestionReport, error) {
	bounded, err := boundResolvedArtist(artist)
	if err != nil {
		return IngestionReport{}, err
	}

	s.mu.Lock()
	if call := s.inflight[bounded.Context.ArtistID]; call != nil {
		s.mu.Unlock()
		select {
		case <-call.done:
			return call.report, call.err
		case <-ctx.Done():
			return IngestionReport{}, ctx.Err()
		}
	}
	call := &ingestionCall{done: make(chan struct{})}
	s.inflight[bounded.Context.ArtistID] = call
	s.mu.Unlock()

	call.report, call.err = s.ingestOnce(ctx, bounded)
	s.mu.Lock()
	delete(s.inflight, bounded.Context.ArtistID)
	close(call.done)
	s.mu.Unlock()
	return call.report, call.err
}

func (s *IngestionService) ingestOnce(ctx context.Context, artist ResolvedArtist) (IngestionReport, error) {
	perArtist := make(chan struct{}, min(maxSourcesPerArtist, len(s.sources)))
	results := make(chan sourceFetchResult, len(s.sources))
	var wg sync.WaitGroup
	for _, source := range s.sources {
		wg.Add(1)
		go func(source BiographySource) {
			defer wg.Done()
			if err := acquireSlot(ctx, perArtist); err != nil {
				results <- sourceFetchResult{provider: source.Provider(), err: err}
				return
			}
			defer releaseSlot(perArtist)
			if err := acquireSlot(ctx, s.global); err != nil {
				results <- sourceFetchResult{provider: source.Provider(), err: err}
				return
			}
			defer releaseSlot(s.global)

			result, err := source.Fetch(ctx, artist)
			results <- sourceFetchResult{provider: source.Provider(), result: result, err: err}
		}(source)
	}
	wg.Wait()
	close(results)

	var report IngestionReport
	for fetched := range results {
		if fetched.err != nil {
			report.Failed++
			continue
		}
		if fetched.result == nil {
			report.Empty++
			continue
		}
		candidate, err := candidateFromSource(artist, fetched.provider, *fetched.result)
		if err != nil {
			report.Failed++
			continue
		}
		if _, err := s.store.PutCandidate(ctx, candidate); err != nil {
			if ctx.Err() != nil {
				return IngestionReport{}, ctx.Err()
			}
			return report, fmt.Errorf("persist %s biography candidate: %w", fetched.provider, err)
		}
		report.Stored++
	}
	if ctx.Err() != nil {
		return IngestionReport{}, ctx.Err()
	}
	return report, nil
}

func boundResolvedArtist(input ResolvedArtist) (ResolvedArtist, error) {
	boundedContext, err := BoundIdentityContext(input.Context)
	if err != nil {
		return ResolvedArtist{}, fmt.Errorf("invalid biography artist context: %w", err)
	}
	identity := input.Identity
	if identity.Status != ResolutionResolved || !isFiniteUnit(identity.Confidence) || identity.Confidence < AutoResolveConfidence {
		return ResolvedArtist{}, fmt.Errorf("biography artist identity is not decisively resolved")
	}
	if !identity.Provider.valid() || identity.Provider == ProviderManual {
		return ResolvedArtist{}, fmt.Errorf("invalid resolved identity provider %q", identity.Provider)
	}
	if err := validateRequired("resolved provider artist id", identity.ProviderArtistID, MaxProviderIDBytes); err != nil {
		return ResolvedArtist{}, err
	}
	if err := validateOptional("resolved musicbrainz id", identity.MusicBrainzID, MaxProviderIDBytes); err != nil {
		return ResolvedArtist{}, err
	}
	if boundedContext.MusicBrainzID != "" && identity.MusicBrainzID != "" &&
		!strings.EqualFold(boundedContext.MusicBrainzID, identity.MusicBrainzID) {
		return ResolvedArtist{}, fmt.Errorf("resolved identity contradicts artist musicbrainz id")
	}
	if err := validateEvidenceCodes(identity.EvidenceCodes); err != nil {
		return ResolvedArtist{}, err
	}
	if len(identity.EvidenceCodes) == 0 {
		return ResolvedArtist{}, fmt.Errorf("resolved identity has no evidence")
	}
	if err := validateEvidenceCodes(identity.ContradictionCodes); err != nil {
		return ResolvedArtist{}, err
	}
	identity.EvidenceCodes = append([]string(nil), identity.EvidenceCodes...)
	identity.ContradictionCodes = append([]string(nil), identity.ContradictionCodes...)
	return ResolvedArtist{Context: boundedContext, Identity: identity}, nil
}

func candidateFromSource(artist ResolvedArtist, provider Provider, result BiographySourceResult) (Candidate, error) {
	if !validBiographyProvider(provider) {
		return Candidate{}, fmt.Errorf("invalid biography provider %q", provider)
	}
	if err := validateRequired("source artist id", result.SourceArtistID, MaxProviderIDBytes); err != nil {
		return Candidate{}, err
	}
	if strings.TrimSpace(result.SourceURL) == "" {
		return Candidate{}, fmt.Errorf("source url is required")
	}
	parsed, err := url.Parse(result.SourceURL)
	if err != nil || parsed.Scheme != "https" {
		return Candidate{}, fmt.Errorf("source url must use https")
	}
	if !isFiniteUnit(result.MatchConfidence) || result.MatchConfidence < AutoResolveConfidence {
		return Candidate{}, fmt.Errorf("source match confidence is below identity threshold")
	}
	if err := validateEvidenceCodes(result.EvidenceCodes); err != nil {
		return Candidate{}, err
	}
	if len(result.EvidenceCodes) == 0 {
		return Candidate{}, fmt.Errorf("biography source has no identity evidence")
	}
	evidence, err := json.Marshal(struct {
		Evidence []string `json:"evidence"`
	}{Evidence: result.EvidenceCodes})
	if err != nil {
		return Candidate{}, fmt.Errorf("encode biography evidence: %w", err)
	}
	now := time.Now()
	candidate := Candidate{
		ArtistID: artist.Context.ArtistID, Provider: provider,
		SourceArtistID: strings.TrimSpace(result.SourceArtistID), SourceURL: result.SourceURL,
		OriginalText: result.OriginalText, OriginalLanguage: strings.TrimSpace(result.LanguageHint),
		MatchConfidence: result.MatchConfidence, Status: CandidateFetched,
		EvidenceJSON: string(evidence), FetchedAt: now, UpdatedAt: now,
	}
	if err := candidate.Validate(); err != nil {
		return Candidate{}, err
	}
	return candidate, nil
}

func validateEvidenceCodes(values []string) error {
	if len(values) > maxBiographyEvidenceCodes {
		return fmt.Errorf("too many biography evidence codes")
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > maxBiographyEvidenceCodeSize {
			return fmt.Errorf("invalid biography evidence code")
		}
	}
	return nil
}

func validBiographyProvider(provider Provider) bool {
	switch provider {
	case ProviderYandexMusic, ProviderDeezer, ProviderAppleMusic, ProviderWikipedia,
		ProviderTheAudioDB, ProviderLastFM, ProviderDiscogs, ProviderOfficial:
		return true
	default:
		return false
	}
}

func acquireSlot(ctx context.Context, slots chan struct{}) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseSlot(slots chan struct{}) {
	<-slots
}

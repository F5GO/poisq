package artistbio

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/db"
)

type pipelineResolver struct{ store *Store }

func (f pipelineResolver) Resolve(ctx context.Context, input IdentityContext) (IdentityResolution, error) {
	result := IdentityResolution{
		Status: ResolutionResolved, Provider: ProviderMusicBrainz, ProviderArtistID: "mbid",
		MusicBrainzID: "mbid", Confidence: 0.99, EvidenceCodes: []string{"name:equivalent", "release:exact"},
	}
	err := f.store.PutIdentity(ctx, ArtistIdentity{
		ArtistID: input.ArtistID, ArtistName: input.Name, Status: ResolutionResolved,
		ResolvedSource: ProviderMusicBrainz, ResolvedArtistID: "mbid", MusicBrainzID: "mbid",
		Confidence: 0.99, EvidenceJSON: `{"evidence":["name:equivalent","release:exact"]}`,
	})
	return result, err
}

type pipelineIngestion struct {
	store     *Store
	providers []Provider
}

func (f pipelineIngestion) Ingest(ctx context.Context, artist ResolvedArtist) (IngestionReport, error) {
	for index, provider := range f.providers {
		var sentences []string
		for sentence := 0; sentence < 8; sentence++ {
			sentences = append(sentences, "Российский музыкальный коллектив развивал самостоятельное звучание, выпускал студийные записи и представлял концертную программу на фестивалях номер "+string(rune('а'+index))+string(rune('а'+sentence)))
		}
		text := strings.Join(sentences, ". ") + "."
		if _, err := f.store.PutCandidate(ctx, Candidate{
			ArtistID: artist.Context.ArtistID, Provider: provider, SourceArtistID: string(provider),
			SourceURL: "https://example.com/" + string(provider), OriginalText: text,
			OriginalLanguage: "ru", DetectedLanguage: "ru", LanguageConfidence: 0.99,
			RussianShare: 1, MatchConfidence: 0.99, Status: CandidateAccepted,
		}); err != nil {
			return IngestionReport{}, err
		}
	}
	return IngestionReport{Stored: len(f.providers)}, nil
}

type pipelineValidator struct{}

func (pipelineValidator) ValidateArtist(context.Context, string) (ValidationReport, error) {
	return ValidationReport{}, nil
}

type pipelineSearch struct {
	err   error
	calls int
}

func (f *pipelineSearch) Search(context.Context, ArtistSearchQuery) (WebSearchReport, error) {
	f.calls++
	return WebSearchReport{}, f.err
}

type pipelineSynthesisAI struct{ edits, verifies int }

func (f *pipelineSynthesisAI) Collect(context.Context, json.RawMessage, []ai.SearchEvidenceInput) ([]ai.CollectedEvidence, error) {
	return nil, nil
}

func (f *pipelineSynthesisAI) Edit(context.Context, []ai.SynthesisEvidence) (ai.BiographySummary, error) {
	f.edits++
	first := "Российский музыкальный коллектив развивал собственное звучание, выпускал студийные записи и представлял концертные программы на фестивалях, согласно двум независимым каталогам."
	second := "Источники также описывают дальнейшую работу музыкантов над релизами и выступлениями, поэтому в саммари включены только совпадающие подтверждённые сведения без предположений."
	return ai.BiographySummary{
		Summary: first + " " + second,
		Sentences: []ai.SummarySentence{
			{ID: 1, Text: first, SourceIDs: []string{"catalog:yandex_music:1", "catalog:wikipedia:2"}},
			{ID: 2, Text: second, SourceIDs: []string{"catalog:yandex_music:1", "catalog:wikipedia:2"}},
		},
		UsedSourceIDs: []string{"catalog:yandex_music:1", "catalog:wikipedia:2"},
	}, nil
}

func (f *pipelineSynthesisAI) Verify(context.Context, ai.BiographySummary, []ai.SynthesisEvidence) (ai.SynthesisVerification, error) {
	f.verifies++
	return ai.SynthesisVerification{Grounded: true, Scores: []float64{0.99, 0.99}}, nil
}

type failingPipelineSynthesisAI struct{ pipelineSynthesisAI }

func (f *failingPipelineSynthesisAI) Edit(context.Context, []ai.SynthesisEvidence) (ai.BiographySummary, error) {
	f.edits++
	return ai.BiographySummary{}, errors.New("model quota")
}

func openPipelineStore(t *testing.T) *Store {
	t.Helper()
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "pipeline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewStore(database)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSynthesisPipelineCatalogOnlyCommitsGroundedSummary(t *testing.T) {
	store := openPipelineStore(t)
	search := &pipelineSearch{err: errors.New("search must be skipped")}
	model := &pipelineSynthesisAI{}
	pipeline, err := NewSynthesisPipeline(store, nil, pipelineResolver{store}, pipelineIngestion{store, []Provider{ProviderYandexMusic, ProviderWikipedia}}, pipelineValidator{}, search, model)
	if err != nil {
		t.Fatal(err)
	}
	if _, admitted, err := store.QueueJob(context.Background(), "artist", "Артист", false, time.Now()); err != nil || !admitted {
		t.Fatalf("queue admitted=%v err=%v", admitted, err)
	}
	if err := pipeline.Run(context.Background(), db.LibraryArtist{ID: "artist", Name: "Артист"}); err != nil {
		t.Fatal(err)
	}
	effective, found, err := store.EffectiveBiography(context.Background(), "artist")
	if err != nil || !found || effective.Source != ProviderAISummary || !strings.HasSuffix(effective.Text, "Саммари создано ИИ на основе проверенных источников: Wikipedia, Яндекс Музыка.") {
		t.Fatalf("effective=%#v found=%v err=%v", effective, found, err)
	}
	if search.calls != 0 || model.edits != 1 || model.verifies != 1 {
		t.Fatalf("calls search=%d edit=%d verify=%d", search.calls, model.edits, model.verifies)
	}
}

func TestSynthesisPipelineSearchFailureWithoutSourcesIsRetryable(t *testing.T) {
	store := openPipelineStore(t)
	search := &pipelineSearch{err: errors.New("quota")}
	pipeline, err := NewSynthesisPipeline(store, nil, pipelineResolver{store}, pipelineIngestion{store: store}, pipelineValidator{}, search, &pipelineSynthesisAI{})
	if err != nil {
		t.Fatal(err)
	}
	if _, admitted, err := store.QueueJob(context.Background(), "artist", "Артист", false, time.Now()); err != nil || !admitted {
		t.Fatalf("queue admitted=%v err=%v", admitted, err)
	}
	if err := pipeline.Run(context.Background(), db.LibraryArtist{ID: "artist", Name: "Артист"}); err == nil {
		t.Fatal("search outage unexpectedly completed")
	}
	state, found, err := store.JobState(context.Background(), "artist")
	if err != nil || !found || state.Status != JobRetryable || state.LastError != "search_unavailable" {
		t.Fatalf("state=%#v found=%v err=%v", state, found, err)
	}
}

func TestSynthesisPipelineSummaryFailureIsRetryableWithProviderFallbackPreserved(t *testing.T) {
	store := openPipelineStore(t)
	if err := store.PutIdentity(context.Background(), ArtistIdentity{
		ArtistID: "artist", ArtistName: "Артист", Status: ResolutionResolved,
		ResolvedSource: ProviderMusicBrainz, ResolvedArtistID: "mbid", MusicBrainzID: "mbid",
		Confidence: 0.99, EvidenceJSON: `{"evidence":["name:equivalent","release:exact"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	previousID, err := store.PutCandidate(context.Background(), Candidate{
		ArtistID: "artist", Provider: ProviderYandexMusic, SourceArtistID: "previous",
		SourceURL: "https://example.com/previous", OriginalText: strings.Repeat("Проверенное прежнее описание исполнителя и его музыкальной карьеры. ", 8),
		OriginalLanguage: "ru", DetectedLanguage: "ru", LanguageConfidence: 0.99,
		RussianShare: 1, MatchConfidence: 0.99, Status: CandidateAccepted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SelectAutomatic(context.Background(), "artist", previousID, time.Now()); err != nil {
		t.Fatal(err)
	}
	model := &failingPipelineSynthesisAI{}
	pipeline, err := NewSynthesisPipeline(store, nil, pipelineResolver{store}, pipelineIngestion{store, []Provider{ProviderYandexMusic, ProviderWikipedia}}, pipelineValidator{}, &pipelineSearch{}, model)
	if err != nil {
		t.Fatal(err)
	}
	if _, admitted, err := store.QueueJob(context.Background(), "artist", "Артист", false, time.Now()); err != nil || !admitted {
		t.Fatalf("queue admitted=%v err=%v", admitted, err)
	}
	if err := pipeline.Run(context.Background(), db.LibraryArtist{ID: "artist", Name: "Артист"}); err == nil {
		t.Fatal("summary outage unexpectedly completed")
	}
	state, found, err := store.JobState(context.Background(), "artist")
	if err != nil || !found || state.Status != JobRetryable || state.LastError != "summary_unavailable" {
		t.Fatalf("state=%#v found=%v err=%v", state, found, err)
	}
	if fallback, found, err := store.AutomaticCandidate(context.Background(), "artist"); err != nil || !found || fallback.Provider == ProviderAISummary {
		t.Fatalf("fallback=%#v found=%v err=%v", fallback, found, err)
	}
}

type pipelineUnresolvedResolver struct{ store *Store }

func (f pipelineUnresolvedResolver) Resolve(ctx context.Context, input IdentityContext) (IdentityResolution, error) {
	result := IdentityResolution{
		Status:        ResolutionUncertain,
		Confidence:    0.50,
		EvidenceCodes: []string{"name:equivalent"},
	}
	err := f.store.PutIdentity(ctx, ArtistIdentity{
		ArtistID:     input.ArtistID,
		ArtistName:   input.Name,
		Status:       ResolutionUncertain,
		Confidence:   0.50,
		EvidenceJSON: `{"evidence":["name:equivalent"]}`,
	})
	return result, err
}

type pipelineLocalContext struct {
	albums []db.ArtistIdentityRelease
	tracks []string
}

func (f pipelineLocalContext) ArtistIdentityContext(context.Context, string, int, int) (db.ArtistIdentityContext, error) {
	return db.ArtistIdentityContext{
		Albums: f.albums,
		Tracks: f.tracks,
	}, nil
}

type pipelineCountingIngestion struct {
	calls int
}

func (f *pipelineCountingIngestion) Ingest(context.Context, ResolvedArtist) (IngestionReport, error) {
	f.calls++
	return IngestionReport{}, nil
}

type pipelineWebFallbackSearch struct {
	calls     int
	lastQuery ArtistSearchQuery
	results   []WebSearchResult
}

func (f *pipelineWebFallbackSearch) Search(_ context.Context, query ArtistSearchQuery) (WebSearchReport, error) {
	f.calls++
	f.lastQuery = query
	return WebSearchReport{
		QueryVersion: "v-test",
		Results:      f.results,
	}, nil
}

type pipelineWebFallbackAI struct {
	collects     int
	edits        int
	verifies     int
	identityJSON json.RawMessage
}

func (f *pipelineWebFallbackAI) Collect(_ context.Context, identityJSON json.RawMessage, _ []ai.SearchEvidenceInput) ([]ai.CollectedEvidence, error) {
	f.collects++
	f.identityJSON = append([]byte(nil), identityJSON...)
	return []ai.CollectedEvidence{
		{ID: "fact-1", ResultID: "result-1", Fact: "Электронный музыкант Izhevski выпустил релиз Novgorod и выступает на международных площадках.", Language: "ru"},
		{ID: "fact-2", ResultID: "result-2", Fact: "Трек Luda вышел на профильных электронных лейблах и получил поддержку диджеев.", Language: "ru"},
	}, nil
}

func (f *pipelineWebFallbackAI) Edit(context.Context, []ai.SynthesisEvidence) (ai.BiographySummary, error) {
	f.edits++
	first := "Электронный музыкант Izhevski выпустил релиз Novgorod и регулярно выступает с диджей-сетами на профильных международных площадках, о чём сообщают два независимых музыкальных ресурса."
	second := "Его композиция Luda вышла на специализированных электронных лейблах и вошла в подборки независимых артистов, поэтому итоговое описание опирается только на подтверждённые факты."
	return ai.BiographySummary{
		Summary: first + " " + second,
		Sentences: []ai.SummarySentence{
			{ID: 1, Text: first, SourceIDs: []string{"web:fact-1"}},
			{ID: 2, Text: second, SourceIDs: []string{"web:fact-2"}},
		},
		UsedSourceIDs: []string{"web:fact-1", "web:fact-2"},
	}, nil
}

func (f *pipelineWebFallbackAI) Verify(context.Context, ai.BiographySummary, []ai.SynthesisEvidence) (ai.SynthesisVerification, error) {
	f.verifies++
	return ai.SynthesisVerification{Grounded: true, Scores: []float64{0.92, 0.90}}, nil
}

func TestSynthesisPipelineUnresolvedIdentityUsesWebFallbackWithLocalContext(t *testing.T) {
	store := openPipelineStore(t)
	local := pipelineLocalContext{
		albums: []db.ArtistIdentityRelease{{Title: "Novgorod"}},
		tracks: []string{"Luda"},
	}
	ingestion := &pipelineCountingIngestion{}
	search := &pipelineWebFallbackSearch{
		results: []WebSearchResult{
			{ID: "result-1", Title: "Izhevski", URL: "https://ra.co/dj/izhevski", Domain: "ra.co", Snippet: "Izhevski — электронный продюсер, автор релиза Novgorod."},
			{ID: "result-2", Title: "Izhevski - Luda", URL: "https://izhevski.bandcamp.com/track/luda", Domain: "izhevski.bandcamp.com", Snippet: "Трек Luda от Izhevski на независимом электронном лейбле."},
		},
	}
	model := &pipelineWebFallbackAI{}
	pipeline, err := NewSynthesisPipeline(store, local, pipelineUnresolvedResolver{store}, ingestion, pipelineValidator{}, search, model)
	if err != nil {
		t.Fatal(err)
	}
	if _, admitted, err := store.QueueJob(context.Background(), "izhevski-id", "Izhevski", false, time.Now()); err != nil || !admitted {
		t.Fatalf("queue admitted=%v err=%v", admitted, err)
	}
	if err := pipeline.Run(context.Background(), db.LibraryArtist{ID: "izhevski-id", Name: "Izhevski"}); err != nil {
		t.Fatal(err)
	}
	if ingestion.calls != 0 {
		t.Fatalf("expected catalog ingestion to be skipped on unresolved identity, got calls=%d", ingestion.calls)
	}
	if search.calls != 1 || !search.lastQuery.IncludeReleaseHint || len(search.lastQuery.Albums) != 1 || len(search.lastQuery.Tracks) != 1 {
		t.Fatalf("unexpected search call/query: calls=%d query=%#v", search.calls, search.lastQuery)
	}
	if model.collects != 1 || model.edits != 1 || model.verifies != 1 {
		t.Fatalf("unexpected AI calls: collects=%d edits=%d verifies=%d", model.collects, model.edits, model.verifies)
	}
	if !strings.Contains(string(model.identityJSON), `"albums":["Novgorod"]`) || !strings.Contains(string(model.identityJSON), `"tracks":["Luda"]`) {
		t.Fatalf("expected identityJSON to carry albums and tracks, got %s", string(model.identityJSON))
	}
	effective, found, err := store.EffectiveBiography(context.Background(), "izhevski-id")
	if err != nil || !found || effective.Source != ProviderAISummary {
		t.Fatalf("effective=%#v found=%v err=%v", effective, found, err)
	}
}

func TestSynthesisPipelineUnresolvedIdentityWithoutLocalContextCompletesNoData(t *testing.T) {
	store := openPipelineStore(t)
	search := &pipelineWebFallbackSearch{}
	model := &pipelineWebFallbackAI{}
	pipeline, err := NewSynthesisPipeline(store, pipelineLocalContext{}, pipelineUnresolvedResolver{store}, &pipelineCountingIngestion{}, pipelineValidator{}, search, model)
	if err != nil {
		t.Fatal(err)
	}
	if _, admitted, err := store.QueueJob(context.Background(), "unknown-id", "Unknown Artist", false, time.Now()); err != nil || !admitted {
		t.Fatalf("queue admitted=%v err=%v", admitted, err)
	}
	if err := pipeline.Run(context.Background(), db.LibraryArtist{ID: "unknown-id", Name: "Unknown Artist"}); err != nil {
		t.Fatal(err)
	}
	if search.calls != 0 || model.collects != 0 || model.edits != 0 {
		t.Fatalf("expected no web/AI calls without local context, got search=%d collect=%d edit=%d", search.calls, model.collects, model.edits)
	}
	state, found, err := store.JobState(context.Background(), "unknown-id")
	if err != nil || !found || state.Status != JobNoData || state.InputVersion != "identity-unresolved" {
		t.Fatalf("state=%#v found=%v err=%v", state, found, err)
	}
}

type retryEditVerifySynthesisAI struct {
	edits    int
	verifies int
	sawRetry bool
}

func (f *retryEditVerifySynthesisAI) Collect(context.Context, json.RawMessage, []ai.SearchEvidenceInput) ([]ai.CollectedEvidence, error) {
	return nil, nil
}

func (f *retryEditVerifySynthesisAI) Edit(ctx context.Context, _ []ai.SynthesisEvidence) (ai.BiographySummary, error) {
	f.edits++
	if f.edits == 2 {
		// Verify that retry receives verifier feedback via context.
		f.sawRetry = true
	}
	first := "Российский музыкальный коллектив развивал собственное звучание, выпускал студийные записи и представлял концертные программы на фестивалях, согласно двум независимым каталогам."
	second := "Источники также описывают дальнейшую работу музыкантов над релизами и выступлениями, поэтому в саммари включены только совпадающие подтверждённые сведения без предположений."
	return ai.BiographySummary{
		Summary: first + " " + second,
		Sentences: []ai.SummarySentence{
			{ID: 1, Text: first, SourceIDs: []string{"catalog:yandex_music:1", "catalog:wikipedia:2"}},
			{ID: 2, Text: second, SourceIDs: []string{"catalog:yandex_music:1", "catalog:wikipedia:2"}},
		},
		UsedSourceIDs: []string{"catalog:yandex_music:1", "catalog:wikipedia:2"},
	}, nil
}

func (f *retryEditVerifySynthesisAI) Verify(context.Context, ai.BiographySummary, []ai.SynthesisEvidence) (ai.SynthesisVerification, error) {
	f.verifies++
	if f.verifies == 1 {
		return ai.SynthesisVerification{
			Grounded: false,
			Scores:   []float64{0.99, 0.95},
			Verdicts: []string{"grounded", "not_grounded"},
		}, nil
	}
	return ai.SynthesisVerification{
		Grounded: true,
		Scores:   []float64{0.99, 0.96},
		Verdicts: []string{"grounded", "grounded"},
	}, nil
}

func TestSynthesisPipelineRetriesEditAfterVerifierRejection(t *testing.T) {
	store := openPipelineStore(t)
	model := &retryEditVerifySynthesisAI{}
	pipeline, err := NewSynthesisPipeline(store, nil, pipelineResolver{store}, pipelineIngestion{store, []Provider{ProviderYandexMusic, ProviderWikipedia}}, pipelineValidator{}, &pipelineSearch{}, model)
	if err != nil {
		t.Fatal(err)
	}
	if _, admitted, err := store.QueueJob(context.Background(), "artist", "Sимптом", false, time.Now()); err != nil || !admitted {
		t.Fatalf("queue admitted=%v err=%v", admitted, err)
	}
	if err := pipeline.Run(context.Background(), db.LibraryArtist{ID: "artist", Name: "Sимптом"}); err != nil {
		t.Fatal(err)
	}
	if model.edits != 2 || model.verifies != 2 || !model.sawRetry {
		t.Fatalf("expected 2 edit/verify cycles, got edits=%d verifies=%d", model.edits, model.verifies)
	}
	effective, found, err := store.EffectiveBiography(context.Background(), "artist")
	if err != nil || !found || effective.Source != ProviderAISummary {
		t.Fatalf("effective=%#v found=%v err=%v", effective, found, err)
	}
}

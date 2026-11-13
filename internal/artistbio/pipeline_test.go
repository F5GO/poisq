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

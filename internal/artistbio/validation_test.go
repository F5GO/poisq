package artistbio

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/db"
)

type fakeBiographyAI struct {
	classifyResult ai.BiographyLanguageResult
	translation    ai.BiographyTranslation
	grounding      ai.BiographyGroundingResult
	err            error
	calls          atomic.Int32
	wait           <-chan struct{}
}

func (f *fakeBiographyAI) ClassifyLanguage(context.Context, string, []string) (ai.BiographyLanguageResult, error) {
	f.calls.Add(1)
	return f.classifyResult, f.err
}

func (f *fakeBiographyAI) Translate(context.Context, string) (ai.BiographyTranslation, error) {
	f.calls.Add(1)
	if f.wait != nil {
		<-f.wait
	}
	return f.translation, f.err
}

func (f *fakeBiographyAI) VerifyGrounding(context.Context, ai.BiographyTranslation) (ai.BiographyGroundingResult, error) {
	f.calls.Add(1)
	return f.grounding, f.err
}

func openValidationStore(t *testing.T) *Store {
	t.Helper()
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "validation.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewStore(database)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func putValidationCandidate(t *testing.T, store *Store, artistID, sourceID, text string) int64 {
	t.Helper()
	ctx := context.Background()
	if _, found, err := store.Identity(ctx, artistID); err != nil {
		t.Fatal(err)
	} else if !found {
		if err := store.PutIdentity(ctx, ArtistIdentity{ArtistID: artistID, Status: ResolutionResolved, Confidence: 1}); err != nil {
			t.Fatal(err)
		}
	}
	id, err := store.PutCandidate(ctx, Candidate{
		ArtistID: artistID, Provider: ProviderWikipedia, SourceArtistID: sourceID,
		OriginalText: text, MatchConfidence: 0.99, Status: CandidateFetched,
		EvidenceJSON: `{"evidence":["id:musicbrainz"]}`,
	})
	if err != nil {
		t.Fatalf("put candidate: %v", err)
	}
	return id
}

func TestBiographyValidatorAcceptsClearRussianWithoutAI(t *testing.T) {
	store := openValidationStore(t)
	text := strings.Repeat("Российская музыкальная группа выпустила известный альбом и выступала на крупных фестивалях. ", 2)
	id := putValidationCandidate(t, store, "artist", "ru", text)
	model := &fakeBiographyAI{}
	validator, err := NewBiographyValidator(store, model, 2)
	if err != nil {
		t.Fatal(err)
	}
	report, err := validator.ValidateArtist(context.Background(), "artist")
	if err != nil || report.Accepted != 1 || model.calls.Load() != 0 {
		t.Fatalf("unexpected report=%#v calls=%d err=%v", report, model.calls.Load(), err)
	}
	selected, found, err := store.AutomaticCandidate(context.Background(), "artist")
	if err != nil || !found || selected.ID != id || selected.RussianText != "" || selected.DetectedLanguage != "ru" {
		t.Fatalf("unexpected selection: %#v found=%v err=%v", selected, found, err)
	}
}

func TestBiographyValidatorAcceptsOnlyGroundedRussianTranslation(t *testing.T) {
	store := openValidationStore(t)
	source := strings.Repeat("The music group released an album and performed at major festivals. ", 2)
	putValidationCandidate(t, store, "artist", "en", source)
	russian := strings.Repeat("Музыкальная группа выпустила альбом и выступала на крупных фестивалях. ", 2)
	model := &fakeBiographyAI{
		translation: ai.BiographyTranslation{
			Items: []ai.BiographyTranslationItem{{ID: 1, Source: source, Text: russian}}, Text: russian,
		},
		grounding: ai.BiographyGroundingResult{Grounded: true, Confidence: 0.98, EvidenceCodes: []string{"meaning:preserved"}},
	}
	validator, _ := NewBiographyValidator(store, model, 2)
	report, err := validator.ValidateArtist(context.Background(), "artist")
	if err != nil || report.Translated != 1 || report.Accepted != 1 || model.calls.Load() != 2 {
		t.Fatalf("unexpected report=%#v calls=%d err=%v", report, model.calls.Load(), err)
	}
	selected, found, err := store.AutomaticCandidate(context.Background(), "artist")
	if err != nil || !found || selected.RussianText != russian || selected.DetectedLanguage != "en" {
		t.Fatalf("unexpected translated selection: %#v found=%v err=%v", selected, found, err)
	}
}

func TestBiographyValidatorLeavesModelOutageRetryable(t *testing.T) {
	store := openValidationStore(t)
	putValidationCandidate(t, store, "artist", "en", strings.Repeat("The artist released several albums and performed internationally. ", 2))
	model := &fakeBiographyAI{err: errors.New("secret response body")}
	validator, _ := NewBiographyValidator(store, model, 1)
	report, err := validator.ValidateArtist(context.Background(), "artist")
	if err != nil || report.Retryable != 1 {
		t.Fatalf("unexpected report=%#v err=%v", report, err)
	}
	candidates, err := store.Candidates(context.Background(), "artist", 4)
	if err != nil || len(candidates) != 1 || candidates[0].Status != CandidateTranslationPending {
		t.Fatalf("candidate is not retryable: %#v err=%v", candidates, err)
	}
}

func TestBiographyValidatorRejectsUnsafeOrUngroundedText(t *testing.T) {
	store := openValidationStore(t)
	putValidationCandidate(t, store, "artist", "bad", strings.Repeat("слово слово слово слово ", 30))
	validator, _ := NewBiographyValidator(store, &fakeBiographyAI{}, 1)
	report, err := validator.ValidateArtist(context.Background(), "artist")
	if err != nil || report.Rejected != 1 {
		t.Fatalf("unexpected report=%#v err=%v", report, err)
	}
	if _, found, err := store.AutomaticCandidate(context.Background(), "artist"); err != nil || found {
		t.Fatalf("unsafe text selected: found=%v err=%v", found, err)
	}
}

func TestBiographyValidatorCoalescesSameArtist(t *testing.T) {
	store := openValidationStore(t)
	putValidationCandidate(t, store, "artist", "en", strings.Repeat("The artist released several albums and performed internationally. ", 2))
	release := make(chan struct{})
	russian := strings.Repeat("Исполнитель выпустил несколько альбомов и выступал на международных фестивалях. ", 2)
	model := &fakeBiographyAI{
		translation: ai.BiographyTranslation{Items: []ai.BiographyTranslationItem{{ID: 1, Source: "source", Text: russian}}, Text: russian},
		grounding:   ai.BiographyGroundingResult{Grounded: true, Confidence: 0.99, EvidenceCodes: []string{"meaning:preserved"}},
		wait:        release,
	}
	validator, _ := NewBiographyValidator(store, model, 2)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := validator.ValidateArtist(context.Background(), "artist")
			results <- err
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if model.calls.Load() != 2 {
		t.Fatalf("same artist was validated more than once: calls=%d", model.calls.Load())
	}
}

func TestBiographyValidatorToleratesConcurrentCacheReset(t *testing.T) {
	store := openValidationStore(t)
	source := strings.Repeat("The artist released several albums and performed internationally. ", 2)
	putValidationCandidate(t, store, "artist", "en-reset", source)
	release := make(chan struct{})
	russian := strings.Repeat("Исполнитель выпустил несколько альбомов и выступал на международных фестивалях. ", 2)
	model := &fakeBiographyAI{
		translation: ai.BiographyTranslation{Items: []ai.BiographyTranslationItem{{ID: 1, Source: source, Text: russian}}, Text: russian},
		grounding:   ai.BiographyGroundingResult{Grounded: true, Confidence: 0.99, EvidenceCodes: []string{"meaning:preserved"}},
		wait:        release,
	}
	validator, _ := NewBiographyValidator(store, model, 1)
	done := make(chan struct {
		report ValidationReport
		err    error
	}, 1)
	go func() {
		report, err := validator.ValidateArtist(context.Background(), "artist")
		done <- struct {
			report ValidationReport
			err    error
		}{report, err}
	}()
	deadline := time.Now().Add(time.Second)
	for model.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := store.ResetAutomaticCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-done
	if result.err != nil || result.report.Skipped != 1 {
		t.Fatalf("cache reset escaped validator: report=%#v err=%v", result.report, result.err)
	}
}

package artistbio

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/db"
	_ "modernc.org/sqlite"
)

type fakeBackgroundResolver struct {
	mu     sync.Mutex
	calls  []string
	inputs []IdentityContext
	result IdentityResolution
	err    error
}

func (f *fakeBackgroundResolver) Resolve(_ context.Context, input IdentityContext) (IdentityResolution, error) {
	f.mu.Lock()
	f.calls = append(f.calls, input.ArtistID)
	f.inputs = append(f.inputs, input)
	f.mu.Unlock()
	if f.err != nil {
		return IdentityResolution{}, f.err
	}
	if f.result.Status != "" {
		return f.result, nil
	}
	return IdentityResolution{
		Status: ResolutionResolved, Provider: ProviderMusicBrainz,
		ProviderArtistID: exactTestMBID, MusicBrainzID: exactTestMBID,
		Confidence: 1, EvidenceCodes: []string{"id:musicbrainz"},
	}, nil
}

func TestBiographyBackgroundUncertainRetryDoesNotStarveInventory(t *testing.T) {
	store := ingestionTestStore(t)
	if err := store.PutIdentity(context.Background(), ArtistIdentity{
		ArtistID: "a", ArtistName: "Первый", Status: ResolutionUncertain,
		EvidenceJSON: `{}`, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	resolver := &fakeBackgroundResolver{}
	processor, err := NewBackgroundProcessor(store, nil, resolver, &fakeBackgroundIngestion{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	report, err := processor.ProcessBatch(context.Background(), backgroundNavDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 || report.Resolved != 1 || len(resolver.calls) != 1 || resolver.calls[0] != "b" {
		t.Fatalf("fresh uncertain artist blocked inventory: report=%#v calls=%v", report, resolver.calls)
	}

	if _, err := store.db.Exec(`UPDATE artist_bio_identity SET updated_at = ? WHERE artist_id = 'a'`, time.Now().Add(-10*time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	resolver.calls = nil
	processor.batchSize = 1
	if _, err := processor.ProcessBatch(context.Background(), backgroundNavDB(t)); err != nil {
		t.Fatal(err)
	}
	if len(resolver.calls) != 1 || resolver.calls[0] != "a" {
		t.Fatalf("expired uncertain artist was not retried: %v", resolver.calls)
	}
}

func TestBiographyBackgroundReportsUncertainSeparately(t *testing.T) {
	store := ingestionTestStore(t)
	resolver := &fakeBackgroundResolver{result: IdentityResolution{Status: ResolutionUncertain}}
	processor, err := NewBackgroundProcessor(store, nil, resolver, &fakeBackgroundIngestion{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	report, err := processor.ProcessBatch(context.Background(), backgroundNavDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Uncertain != 1 || report.Failed != 0 || report.Resolved != 0 {
		t.Fatalf("uncertain identity counted incorrectly: %#v", report)
	}
}

func TestBiographyBackgroundCreatesTruthfulLocalFallback(t *testing.T) {
	store := ingestionTestStore(t)
	trackRepo := db.NewTrackRepository(store.db)
	if err := trackRepo.UpsertTrack(db.TrackRecord{
		ID: "local-track", FilePath: "/music/local.flac", Artist: "Первый",
		Album: "Локальный альбом", Title: "Локальная песня", Year: 2026,
	}); err != nil {
		t.Fatal(err)
	}
	resolver := &fakeBackgroundResolver{result: IdentityResolution{Status: ResolutionUncertain}}
	processor, err := NewBackgroundProcessor(store, trackRepo, resolver, &fakeBackgroundIngestion{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processor.ProcessBatch(context.Background(), backgroundNavDB(t)); err != nil {
		t.Fatal(err)
	}
	effective, found, err := store.EffectiveBiography(context.Background(), "a")
	if err != nil || !found {
		t.Fatalf("local fallback missing: found=%t err=%v", found, err)
	}
	if effective.Source != ProviderLocalLibrary || effective.Manual ||
		!strings.Contains(effective.Text, "не содержит внешних биографических сведений") ||
		!strings.Contains(effective.Text, "Локальный альбом") || !strings.Contains(effective.Text, "Локальная песня") {
		t.Fatalf("unexpected local fallback: %#v", effective)
	}
	analysis := DetectBiographyLanguage(effective.Text)
	if !analysis.QualityOK || analysis.Language != "ru" || analysis.RussianShare < 0.80 {
		t.Fatalf("local fallback is not verified Russian: %#v", analysis)
	}
}

type fakeBackgroundIngestion struct {
	mu    sync.Mutex
	calls []string
}

type fakeBackgroundValidator struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeBackgroundValidator) ValidateArtist(_ context.Context, artistID string) (ValidationReport, error) {
	f.mu.Lock()
	f.calls = append(f.calls, artistID)
	f.mu.Unlock()
	return ValidationReport{Scanned: 1, Accepted: 1}, nil
}

func (f *fakeBackgroundIngestion) Ingest(_ context.Context, artist ResolvedArtist) (IngestionReport, error) {
	f.mu.Lock()
	f.calls = append(f.calls, artist.Context.ArtistID)
	f.mu.Unlock()
	return IngestionReport{Stored: 1}, nil
}

func backgroundNavDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "navidrome.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE artist(id TEXT PRIMARY KEY, name TEXT NOT NULL);
		INSERT INTO artist(id, name) VALUES ('a', 'Первый'), ('b', 'Второй'), ('c', 'Третий');`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestBiographyBackgroundBatchSkipsFreshAndBoundsWork(t *testing.T) {
	store := ingestionTestStore(t)
	resolvedArtistForIngestion(t, store, "a")
	if _, err := store.PutCandidate(context.Background(), Candidate{
		ArtistID: "a", Provider: ProviderDeezer, SourceArtistID: "1",
		SourceURL: "https://www.deezer.com/artist/1", OriginalText: "Описание",
		OriginalLanguage: "ru", MatchConfidence: 1, Status: CandidateFetched,
		EvidenceJSON: `{"evidence":["id:musicbrainz"]}`, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	resolver := &fakeBackgroundResolver{}
	ingestion := &fakeBackgroundIngestion{}
	trackRepo := db.NewTrackRepository(store.db)
	for _, track := range []db.TrackRecord{
		{ID: "track-b-1", FilePath: "/music/one.flac", Artist: "Второй", Album: "Альбом один", Title: "Песня один", Year: 2020},
		{ID: "track-b-2", FilePath: "/music/two.flac", Artist: "Второй", Album: "Альбом два", Title: "Песня два", Year: 2021},
	} {
		if err := trackRepo.UpsertTrack(track); err != nil {
			t.Fatal(err)
		}
	}
	processor, err := NewBackgroundProcessor(store, trackRepo, resolver, ingestion, 1)
	if err != nil {
		t.Fatal(err)
	}
	report, err := processor.ProcessBatch(context.Background(), backgroundNavDB(t))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if report.Scanned != 2 || report.Skipped != 1 || report.Resolved != 1 || report.Ingested != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if len(resolver.calls) != 1 || resolver.calls[0] != "b" || len(ingestion.calls) != 1 || ingestion.calls[0] != "b" {
		t.Fatalf("unexpected calls resolver=%v ingestion=%v", resolver.calls, ingestion.calls)
	}
	if len(resolver.inputs[0].Albums) != 2 {
		t.Fatalf("local album evidence was not forwarded: %#v", resolver.inputs[0])
	}
}

func TestBiographyBackgroundBatchHonorsCancellation(t *testing.T) {
	store := ingestionTestStore(t)
	processor, err := NewBackgroundProcessor(store, nil, &fakeBackgroundResolver{}, &fakeBackgroundIngestion{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = processor.ProcessBatch(ctx, backgroundNavDB(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestBiographyBackgroundValidatesFetchedCandidateWithoutProviderCalls(t *testing.T) {
	store := ingestionTestStore(t)
	resolvedArtistForIngestion(t, store, "a")
	if _, err := store.PutCandidate(context.Background(), Candidate{
		ArtistID: "a", Provider: ProviderDeezer, SourceArtistID: "1",
		SourceURL:       "https://www.deezer.com/artist/1",
		OriginalText:    strings.Repeat("Российская группа выпустила альбом и выступала на фестивалях. ", 2),
		MatchConfidence: 1, Status: CandidateFetched, EvidenceJSON: `{"evidence":["id:musicbrainz"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	resolver := &fakeBackgroundResolver{}
	ingestion := &fakeBackgroundIngestion{}
	validator := &fakeBackgroundValidator{}
	processor, err := NewBackgroundProcessor(store, nil, resolver, ingestion, 1)
	if err != nil {
		t.Fatal(err)
	}
	processor.SetValidator(validator)
	report, err := processor.ProcessBatch(context.Background(), backgroundNavDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Validated != 1 || report.Resolved != 0 || report.Ingested != 0 {
		t.Fatalf("unexpected report: %#v", report)
	}
	if len(validator.calls) != 1 || validator.calls[0] != "a" || len(resolver.calls) != 0 || len(ingestion.calls) != 0 {
		t.Fatalf("unexpected calls validator=%v resolver=%v ingestion=%v", validator.calls, resolver.calls, ingestion.calls)
	}
}

package artistbio

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/db"
)

type fakeBiographySource struct {
	provider Provider
	result   *BiographySourceResult
	err      error
	wait     <-chan struct{}
	calls    atomic.Int32
	active   *atomic.Int32
	max      *atomic.Int32
}

func (s *fakeBiographySource) Provider() Provider { return s.provider }

func (s *fakeBiographySource) Fetch(ctx context.Context, _ ResolvedArtist) (*BiographySourceResult, error) {
	s.calls.Add(1)
	if s.active != nil {
		current := s.active.Add(1)
		defer s.active.Add(-1)
		for {
			previous := s.max.Load()
			if current <= previous || s.max.CompareAndSwap(previous, current) {
				break
			}
		}
	}
	if s.wait != nil {
		select {
		case <-s.wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.result == nil {
		return nil, s.err
	}
	copyResult := *s.result
	copyResult.EvidenceCodes = append([]string(nil), s.result.EvidenceCodes...)
	return &copyResult, s.err
}

func ingestionTestStore(t *testing.T) *Store {
	t.Helper()
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewStore(database)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func resolvedArtistForIngestion(t *testing.T, store *Store, id string) ResolvedArtist {
	t.Helper()
	artist := ResolvedArtist{
		Context: IdentityContext{ArtistID: id, Name: "Кино", MusicBrainzID: "mbid-kino"},
		Identity: IdentityResolution{
			Status: ResolutionResolved, CandidateID: "musicbrainz:mbid-kino",
			Provider: ProviderMusicBrainz, ProviderArtistID: "mbid-kino", MusicBrainzID: "mbid-kino",
			Confidence: 1, EvidenceCodes: []string{"id:musicbrainz"},
		},
	}
	if err := store.PutIdentity(context.Background(), ArtistIdentity{
		ArtistID: id, ArtistName: artist.Context.Name, MusicBrainzID: artist.Context.MusicBrainzID,
		Status: ResolutionResolved, ResolvedSource: ProviderMusicBrainz, ResolvedArtistID: "mbid-kino",
		Confidence: 1, EvidenceJSON: `{"evidence":["id:musicbrainz"]}`,
	}); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	return artist
}

func validBiographyResult(id, text string) *BiographySourceResult {
	return &BiographySourceResult{
		SourceArtistID: id, SourceURL: "https://example.com/artist/" + id,
		OriginalText: text, LanguageHint: "ru", MatchConfidence: 0.99,
		EvidenceCodes: []string{"album:multiple"},
	}
}

func TestIngestionIsolatesProvidersAndPersistsValidCandidate(t *testing.T) {
	store := ingestionTestStore(t)
	artist := resolvedArtistForIngestion(t, store, "artist-1")
	sources := []BiographySource{
		&fakeBiographySource{provider: ProviderYandexMusic, result: validBiographyResult("ya-1", "Настоящее описание")},
		&fakeBiographySource{provider: ProviderDeezer},
		&fakeBiographySource{provider: ProviderAppleMusic, err: errors.New("provider-secret-body")},
		&fakeBiographySource{provider: ProviderWikipedia, result: validBiographyResult("wiki-1", strings.Repeat("x", MaxBiographyBytes+1))},
	}
	service, err := NewIngestionService(store, sources, 8)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	report, err := service.Ingest(context.Background(), artist)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if report.Stored != 1 || report.Empty != 1 || report.Failed != 2 {
		t.Fatalf("unexpected report: %#v", report)
	}
	candidate, found, err := store.Candidate(context.Background(), "artist-1", ProviderYandexMusic, "ya-1")
	if err != nil || !found {
		t.Fatalf("stored candidate: found=%v err=%v", found, err)
	}
	if candidate.OriginalText != "Настоящее описание" || candidate.Status != CandidateFetched || candidate.OriginalLanguage != "ru" {
		t.Fatalf("unexpected candidate: %#v", candidate)
	}
}

func TestIngestionLimitsPerArtistConcurrency(t *testing.T) {
	store := ingestionTestStore(t)
	artist := resolvedArtistForIngestion(t, store, "artist-1")
	release := make(chan struct{})
	var active, maximum atomic.Int32
	providers := []Provider{ProviderYandexMusic, ProviderDeezer, ProviderAppleMusic, ProviderWikipedia, ProviderTheAudioDB, ProviderLastFM}
	sources := make([]BiographySource, 0, len(providers))
	for i, provider := range providers {
		sources = append(sources, &fakeBiographySource{
			provider: provider, result: validBiographyResult(string(rune('a'+i)), "Описание"),
			wait: release, active: &active, max: &maximum,
		})
	}
	service, err := NewIngestionService(store, sources, 8)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.Ingest(context.Background(), artist)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for maximum.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := maximum.Load(); got != 4 {
		t.Fatalf("active provider calls = %d, want 4", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if maximum.Load() > 4 {
		t.Fatalf("per-artist concurrency exceeded: %d", maximum.Load())
	}
}

func TestIngestionCoalescesConcurrentWork(t *testing.T) {
	store := ingestionTestStore(t)
	artist := resolvedArtistForIngestion(t, store, "artist-1")
	release := make(chan struct{})
	source := &fakeBiographySource{
		provider: ProviderDeezer, result: validBiographyResult("deezer-1", "Описание"), wait: release,
	}
	service, err := NewIngestionService(store, []BiographySource{source}, 2)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	first := make(chan IngestionReport, 1)
	go func() {
		report, _ := service.Ingest(context.Background(), artist)
		first <- report
	}()
	deadline := time.Now().Add(time.Second)
	for source.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	waiter := make(chan IngestionReport, 1)
	go func() {
		report, _ := service.Ingest(context.Background(), artist)
		waiter <- report
	}()
	time.Sleep(10 * time.Millisecond)
	close(release)
	if report := <-first; report.Stored != 1 {
		t.Fatalf("owner report = %#v", report)
	}
	if report := <-waiter; report.Stored != 1 {
		t.Fatalf("waiter report = %#v", report)
	}
	if source.calls.Load() != 1 {
		t.Fatalf("provider calls were not coalesced: %d", source.calls.Load())
	}
}

func TestIngestionCancellationClearsInflightWork(t *testing.T) {
	store := ingestionTestStore(t)
	artist := resolvedArtistForIngestion(t, store, "artist-1")
	release := make(chan struct{})
	source := &fakeBiographySource{
		provider: ProviderDeezer, result: validBiographyResult("deezer-1", "Описание"), wait: release,
	}
	service, err := NewIngestionService(store, []BiographySource{source}, 1)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.Ingest(ctx, artist)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for source.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ingestion = %v", err)
	}
	close(release)
	report, err := service.Ingest(context.Background(), artist)
	if err != nil || report.Stored != 1 {
		t.Fatalf("retry after cancellation: report=%#v err=%v", report, err)
	}
	if source.calls.Load() != 2 {
		t.Fatalf("cancelled in-flight call was retained: %d", source.calls.Load())
	}
}

func TestIngestionRejectsUnresolvedArtistWithoutProviderCalls(t *testing.T) {
	store := ingestionTestStore(t)
	source := &fakeBiographySource{provider: ProviderDeezer, result: validBiographyResult("1", "Описание")}
	service, err := NewIngestionService(store, []BiographySource{source}, 1)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	_, err = service.Ingest(context.Background(), ResolvedArtist{
		Context:  IdentityContext{ArtistID: "artist", Name: "Тёзка"},
		Identity: IdentityResolution{Status: ResolutionUncertain, Confidence: 0.7},
	})
	if err == nil {
		t.Fatal("unresolved artist accepted")
	}
	if source.calls.Load() != 0 {
		t.Fatalf("provider called for unresolved artist: %d", source.calls.Load())
	}
}

func TestIngestionRefreshUpdatesOneCandidate(t *testing.T) {
	store := ingestionTestStore(t)
	artist := resolvedArtistForIngestion(t, store, "artist-1")
	source := &fakeBiographySource{provider: ProviderDeezer, result: validBiographyResult("deezer-1", "Первая версия")}
	service, err := NewIngestionService(store, []BiographySource{source}, 1)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := service.Ingest(context.Background(), artist); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	first, found, err := store.Candidate(context.Background(), "artist-1", ProviderDeezer, "deezer-1")
	if err != nil || !found {
		t.Fatalf("first candidate: found=%v err=%v", found, err)
	}
	source.result = validBiographyResult("deezer-1", "Вторая версия")
	if _, err := service.Ingest(context.Background(), artist); err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	second, found, err := store.Candidate(context.Background(), "artist-1", ProviderDeezer, "deezer-1")
	if err != nil || !found {
		t.Fatalf("second candidate: found=%v err=%v", found, err)
	}
	if second.ID != first.ID || second.OriginalText != "Вторая версия" {
		t.Fatalf("candidate was not updated in place: first=%#v second=%#v", first, second)
	}
}

func TestIngestionConstructorRejectsDuplicateOrInvalidSources(t *testing.T) {
	store := ingestionTestStore(t)
	tests := map[string][]BiographySource{
		"duplicate": {
			&fakeBiographySource{provider: ProviderDeezer},
			&fakeBiographySource{provider: ProviderDeezer},
		},
		"manual":    {&fakeBiographySource{provider: ProviderManual}},
		"navidrome": {&fakeBiographySource{provider: ProviderNavidrome}},
		"unknown":   {&fakeBiographySource{provider: Provider("unknown")}},
		"nil":       {nil},
	}
	for name, sources := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewIngestionService(store, sources, 1); err == nil {
				t.Fatal("invalid source set accepted")
			}
		})
	}
}

func TestIngestionRejectsUnsafeSourceResults(t *testing.T) {
	store := ingestionTestStore(t)
	artist := resolvedArtistForIngestion(t, store, "artist-1")
	tests := map[string]*BiographySourceResult{
		"insecure url": {
			SourceArtistID: "1", SourceURL: "http://example.com/artist/1", OriginalText: "Описание",
			MatchConfidence: 1, EvidenceCodes: []string{"id:provider"},
		},
		"userinfo url": {
			SourceArtistID: "1", SourceURL: "https://user:pass@example.com/artist/1", OriginalText: "Описание",
			MatchConfidence: 1, EvidenceCodes: []string{"id:provider"},
		},
		"nan confidence": {
			SourceArtistID: "1", SourceURL: "https://example.com/artist/1", OriginalText: "Описание",
			MatchConfidence: math.NaN(), EvidenceCodes: []string{"id:provider"},
		},
		"no evidence": {
			SourceArtistID: "1", SourceURL: "https://example.com/artist/1", OriginalText: "Описание",
			MatchConfidence: 1,
		},
		"too many evidence codes": {
			SourceArtistID: "1", SourceURL: "https://example.com/artist/1", OriginalText: "Описание",
			MatchConfidence: 1, EvidenceCodes: make([]string, maxBiographyEvidenceCodes+1),
		},
	}
	for name, result := range tests {
		t.Run(name, func(t *testing.T) {
			source := &fakeBiographySource{provider: ProviderDeezer, result: result}
			service, err := NewIngestionService(store, []BiographySource{source}, 1)
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			report, err := service.Ingest(context.Background(), artist)
			if err != nil {
				t.Fatalf("ingest invalid result: %v", err)
			}
			if report.Failed != 1 || report.Stored != 0 {
				t.Fatalf("unsafe result accepted: %#v", report)
			}
		})
	}
}

func TestIngestionGlobalConcurrencyLimit(t *testing.T) {
	store := ingestionTestStore(t)
	artistOne := resolvedArtistForIngestion(t, store, "artist-1")
	artistTwo := resolvedArtistForIngestion(t, store, "artist-2")
	release := make(chan struct{})
	var active, maximum atomic.Int32
	sources := []BiographySource{
		&fakeBiographySource{provider: ProviderYandexMusic, result: validBiographyResult("ya", "Описание"), wait: release, active: &active, max: &maximum},
		&fakeBiographySource{provider: ProviderDeezer, result: validBiographyResult("dz", "Описание"), wait: release, active: &active, max: &maximum},
	}
	service, err := NewIngestionService(store, sources, 2)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	for _, artist := range []ResolvedArtist{artistOne, artistTwo} {
		go func(value ResolvedArtist) {
			defer wg.Done()
			_, _ = service.Ingest(context.Background(), value)
		}(artist)
	}
	deadline := time.Now().Add(time.Second)
	for maximum.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if got := maximum.Load(); got != 2 {
		t.Fatalf("global provider concurrency = %d, want 2", got)
	}
}

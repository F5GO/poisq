package artistbio

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/metadata"
)

type fakeIdentitySource struct {
	mu         sync.Mutex
	calls      int
	candidates []IdentityCandidate
	err        error
	wait       <-chan struct{}
}

func (f *fakeIdentitySource) FindIdentityCandidates(ctx context.Context, _ IdentityContext) ([]IdentityCandidate, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.wait != nil {
		select {
		case <-f.wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return append([]IdentityCandidate(nil), f.candidates...), f.err
}

func (f *fakeIdentitySource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeIdentityArbiter struct {
	mu       sync.Mutex
	calls    int
	decision ai.IdentityArbitrationDecision
	err      error
}

func (f *fakeIdentityArbiter) ArbitrateIdentity(_ context.Context, _ ai.IdentityArbitrationInput) (ai.IdentityArbitrationDecision, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.decision, f.err
}

func resolverStore(t *testing.T) *Store {
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

func TestResolverExactIDSkipsAIAndCaches(t *testing.T) {
	store := resolverStore(t)
	source := &fakeIdentitySource{candidates: []IdentityCandidate{{
		ID: "mb:1", Provider: ProviderMusicBrainz, ProviderArtistID: "mbid", MusicBrainzID: "mbid", Name: "Кино",
	}}}
	arbiter := &fakeIdentityArbiter{}
	resolver := NewIdentityResolver(store, nil, source, arbiter)
	ctx := IdentityContext{ArtistID: "nav-1", Name: "Кино", MusicBrainzID: "mbid"}

	first, err := resolver.Resolve(context.Background(), ctx)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if first.Status != ResolutionResolved || first.CandidateID != "mb:1" || arbiter.calls != 0 {
		t.Fatalf("unexpected exact result: %#v ai_calls=%d", first, arbiter.calls)
	}
	second, err := resolver.Resolve(context.Background(), ctx)
	if err != nil || second.Status != ResolutionResolved {
		t.Fatalf("cached resolve: result=%#v err=%v", second, err)
	}
	if source.callCount() != 1 {
		t.Fatalf("resolved cache did not skip provider: %d calls", source.callCount())
	}
}

func TestResolverUsesAIOnlyForDecisiveAmbiguity(t *testing.T) {
	store := resolverStore(t)
	albums := []IdentityRelease{{Title: "Один", Year: 2020}, {Title: "Два", Year: 2021}}
	source := &fakeIdentitySource{candidates: []IdentityCandidate{
		{ID: "one", Provider: ProviderMusicBrainz, ProviderArtistID: "one", Name: "Луна", Albums: albums},
		{ID: "two", Provider: ProviderDeezer, ProviderArtistID: "two", Name: "Луна", Albums: albums},
	}}
	arbiter := &fakeIdentityArbiter{decision: ai.IdentityArbitrationDecision{
		Decision: "same_artist", CandidateID: "one", Confidence: 0.98,
		EvidenceCodes: []string{"album:overlap"},
	}}
	resolver := NewIdentityResolver(store, nil, source, arbiter)
	result, err := resolver.Resolve(context.Background(), IdentityContext{ArtistID: "nav-1", Name: "Луна", Albums: albums})
	if err != nil {
		t.Fatalf("resolve ambiguity: %v", err)
	}
	if result.Status != ResolutionResolved || result.CandidateID != "one" || arbiter.calls != 1 {
		t.Fatalf("AI ambiguity result: %#v calls=%d", result, arbiter.calls)
	}
}

func TestResolverUsesAIForExactNameAndReleaseEvidence(t *testing.T) {
	store := resolverStore(t)
	albums := []IdentityRelease{{Title: "Группа крови", Year: 1988}}
	source := &fakeIdentitySource{candidates: []IdentityCandidate{{
		ID: "kino", Provider: ProviderMusicBrainz, ProviderArtistID: "kino-mbid", MusicBrainzID: "kino-mbid",
		Name: "Кино", Albums: albums, ProviderScore: 1,
	}}}
	arbiter := &fakeIdentityArbiter{decision: ai.IdentityArbitrationDecision{
		Decision: "same_artist", CandidateID: "kino", Confidence: 0.99,
		EvidenceCodes: []string{"name:equivalent", "album:overlap"},
	}}
	resolver := NewIdentityResolver(store, nil, source, arbiter)
	result, err := resolver.Resolve(context.Background(), IdentityContext{ArtistID: "nav-kino", Name: "Кино", Albums: albums})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ResolutionResolved || result.CandidateID != "kino" || arbiter.calls != 1 {
		t.Fatalf("AI did not confirm evidence-backed candidate: result=%#v calls=%d", result, arbiter.calls)
	}
	if result.Confidence != 0.99 {
		t.Fatalf("AI confidence was not applied: %#v", result)
	}
}

type fakeMusicBrainzIdentityClient struct {
	artists    []metadata.MBArtistInfo
	releases   map[string][]metadata.MBReleaseGroupInfo
	recordings map[string][]metadata.MBRecordingInfo
	browsed    []string
	limit      int
}

func (f *fakeMusicBrainzIdentityClient) SearchArtists(context.Context, string, int) ([]metadata.MBArtistInfo, error) {
	return append([]metadata.MBArtistInfo(nil), f.artists...), nil
}

func (f *fakeMusicBrainzIdentityClient) BrowseArtistReleaseGroups(_ context.Context, artistID string, limit int) ([]metadata.MBReleaseGroupInfo, error) {
	f.browsed = append(f.browsed, artistID)
	f.limit = limit
	return append([]metadata.MBReleaseGroupInfo(nil), f.releases[artistID]...), nil
}

func (f *fakeMusicBrainzIdentityClient) SearchArtistRecordings(_ context.Context, artistID string, _ []string, limit int) ([]metadata.MBRecordingInfo, error) {
	f.limit = limit
	return append([]metadata.MBRecordingInfo(nil), f.recordings[artistID]...), nil
}

func TestMusicBrainzIdentitySourceUsesCanonicalDiscography(t *testing.T) {
	client := &fakeMusicBrainzIdentityClient{
		artists: []metadata.MBArtistInfo{
			{ArtistMBID: "kino", CanonicalName: "Кино", Score: 100},
			{ArtistMBID: "other", CanonicalName: "Cinema", Score: 90},
		},
		releases: map[string][]metadata.MBReleaseGroupInfo{
			"kino": {{ReleaseGroupMBID: "release", Title: "Группа крови", Year: 1988}},
		},
	}
	source := NewMusicBrainzIdentitySource(client)
	candidates, err := source.FindIdentityCandidates(context.Background(), IdentityContext{
		ArtistID: "nav-kino", Name: "Кино", Albums: []IdentityRelease{{Title: "Группа крови", Year: 1988}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.browsed) != 1 || client.browsed[0] != "kino" {
		t.Fatalf("unexpected discography lookups: %v", client.browsed)
	}
	if client.limit != 100 {
		t.Fatalf("discography browse limit = %d", client.limit)
	}
	if len(candidates) != 2 || len(candidates[0].Albums) != 1 || candidates[0].Albums[0].Title != "Группа крови" {
		t.Fatalf("canonical discography missing: %#v", candidates)
	}
	result := ResolveIdentityDeterministically(IdentityContext{
		ArtistID: "nav-kino", Name: "Кино", Albums: []IdentityRelease{{Title: "Группа крови", Year: 1988}},
	}, candidates)
	if !result.RequiresAI || result.Confidence < 0.69 {
		t.Fatalf("release-backed candidate was not made eligible for arbitration: %#v", result)
	}
}

func TestMusicBrainzIdentitySourceFallsBackToCanonicalRecordings(t *testing.T) {
	client := &fakeMusicBrainzIdentityClient{
		artists: []metadata.MBArtistInfo{{ArtistMBID: "artist", CanonicalName: "Артист", Score: 100}},
		recordings: map[string][]metadata.MBRecordingInfo{
			"artist": {
				{RecordingMBID: "one", Title: "Первый трек"},
				{RecordingMBID: "two", Title: "Второй трек"},
				{RecordingMBID: "other", Title: "Чужой трек"},
			},
		},
	}
	source := NewMusicBrainzIdentitySource(client)
	input := IdentityContext{ArtistID: "nav", Name: "Артист", Tracks: []string{"Первый трек", "Второй трек"}}
	candidates, err := source.FindIdentityCandidates(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || len(candidates[0].Tracks) != 2 {
		t.Fatalf("canonical recording matches missing: %#v", candidates)
	}
	result := ResolveIdentityDeterministically(input, candidates)
	if result.Status != ResolutionResolved || result.Confidence < AutoResolveConfidence {
		t.Fatalf("two real recording matches did not resolve identity: %#v", result)
	}
}

func TestResolverNeverSendsNameOnlyHomonymToAI(t *testing.T) {
	store := resolverStore(t)
	source := &fakeIdentitySource{candidates: []IdentityCandidate{{
		ID: "luna", Provider: ProviderMusicBrainz, ProviderArtistID: "luna-mbid", Name: "Луна", ProviderScore: 1,
	}}}
	arbiter := &fakeIdentityArbiter{}
	resolver := NewIdentityResolver(store, nil, source, arbiter)
	result, err := resolver.Resolve(context.Background(), IdentityContext{ArtistID: "nav-luna", Name: "Луна"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ResolutionUncertain || arbiter.calls != 0 {
		t.Fatalf("name-only homonym reached AI: result=%#v calls=%d", result, arbiter.calls)
	}
}

func TestFallbackIdentitySourceUsesRegionalCatalogOnlyWithoutPrimaryEvidence(t *testing.T) {
	input := IdentityContext{ArtistID: "artist", Name: "ANIKV", Albums: []IdentityRelease{{Title: "Киносеанс"}}}
	primary := &fakeIdentitySource{candidates: []IdentityCandidate{{
		ID: "mb", Provider: ProviderMusicBrainz, ProviderArtistID: "mb", Name: "ANIKV", ProviderScore: 1,
	}}}
	secondary := &fakeIdentitySource{candidates: []IdentityCandidate{{
		ID: "ya", Provider: ProviderYandexMusic, ProviderArtistID: "1", Name: "ANIKV",
		Albums: []IdentityRelease{{Title: "Киносеанс"}},
	}}}
	source := NewFallbackIdentitySource(primary, secondary)
	candidates, err := source.FindIdentityCandidates(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Provider != ProviderYandexMusic || secondary.callCount() != 1 {
		t.Fatalf("regional grounded candidate was not selected: %#v calls=%d", candidates, secondary.callCount())
	}

	primary.candidates[0].Albums = []IdentityRelease{{Title: "Киносеанс"}}
	secondary.calls = 0
	candidates, err = source.FindIdentityCandidates(context.Background(), input)
	if err != nil || len(candidates) != 1 || candidates[0].Provider != ProviderMusicBrainz || secondary.callCount() != 0 {
		t.Fatalf("regional fallback ran despite primary evidence: %#v calls=%d err=%v", candidates, secondary.callCount(), err)
	}
}

func TestResolverOutageRemainsUncertain(t *testing.T) {
	store := resolverStore(t)
	source := &fakeIdentitySource{err: errors.New("provider unavailable")}
	resolver := NewIdentityResolver(store, nil, source, nil)
	result, err := resolver.Resolve(context.Background(), IdentityContext{ArtistID: "nav-1", Name: "Неизвестный"})
	if err != nil {
		t.Fatalf("provider outage escaped pipeline: %v", err)
	}
	if result.Status != ResolutionUncertain || result.CandidateID != "" {
		t.Fatalf("provider outage was guessed: %#v", result)
	}
}

func TestResolverCoalescesConcurrentWork(t *testing.T) {
	store := resolverStore(t)
	release := make(chan struct{})
	source := &fakeIdentitySource{wait: release, candidates: []IdentityCandidate{{
		ID: "mb:1", Provider: ProviderMusicBrainz, ProviderArtistID: "mbid", MusicBrainzID: "mbid", Name: "Кино",
	}}}
	resolver := NewIdentityResolver(store, nil, source, nil)
	ctx := IdentityContext{ArtistID: "nav-1", Name: "Кино", MusicBrainzID: "mbid"}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			result, err := resolver.Resolve(context.Background(), ctx)
			if err != nil || result.Status != ResolutionResolved {
				errs <- errors.New("coalesced resolution failed")
			}
		}()
	}
	deadline := time.Now().Add(time.Second)
	for source.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if source.callCount() != 1 {
		t.Fatalf("provider calls were not coalesced: %d", source.callCount())
	}
}

func TestResolverCancellationClearsInflightEntry(t *testing.T) {
	store := resolverStore(t)
	release := make(chan struct{})
	source := &fakeIdentitySource{wait: release, candidates: []IdentityCandidate{{
		ID: "mb:1", Provider: ProviderMusicBrainz, ProviderArtistID: "mbid", MusicBrainzID: "mbid", Name: "Кино",
	}}}
	resolver := NewIdentityResolver(store, nil, source, nil)
	input := IdentityContext{ArtistID: "nav-1", Name: "Кино", MusicBrainzID: "mbid"}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := resolver.Resolve(ctx, input)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for source.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resolution returned %v", err)
	}

	close(release)
	result, err := resolver.Resolve(context.Background(), input)
	if err != nil || result.Status != ResolutionResolved {
		t.Fatalf("retry after cancellation: result=%#v err=%v", result, err)
	}
	if source.callCount() != 2 {
		t.Fatalf("cancelled in-flight entry was retained: %d calls", source.callCount())
	}
}

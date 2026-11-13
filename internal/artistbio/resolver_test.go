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

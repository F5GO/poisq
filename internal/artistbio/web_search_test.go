package artistbio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/keypool"
)

type fakeSearchKeys struct {
	key         *keypool.LeasedKey
	success     int
	rateLimited int
	invalid     int
}

func (f *fakeSearchKeys) Acquire(string) (*keypool.LeasedKey, error) { return f.key, nil }
func (f *fakeSearchKeys) ReportSuccess(int64)                        { f.success++ }
func (f *fakeSearchKeys) ReportRateLimit429(int64, string)           { f.rateLimited++ }
func (f *fakeSearchKeys) ReportUnavailable503(int64, string)         {}
func (f *fakeSearchKeys) ReportInvalid(int64, int, string)           { f.invalid++ }

func TestTavilySearchBoundsAndFiltersResults(t *testing.T) {
	keys := &fakeSearchKeys{key: &keypool.LeasedKey{ID: 7, PlainKey: "secret-tavily-key"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || r.Header.Get("Authorization") != "Bearer secret-tavily-key" || request["api_key"] != nil || request["search_depth"] != "basic" || request["include_raw_content"] != false {
			t.Fatalf("unsafe request payload: %#v err=%v", request, err)
		}
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Official biography","url":"https://artist.example/bio","content":"A sufficiently detailed factual snippet about the music artist, releases, performances, and career history.","score":0.9},
			{"title":"Private","url":"https://127.0.0.1/admin","content":"A sufficiently long private-network result that must never be accepted as public evidence.","score":1},
			{"title":"Forum","url":"https://reddit.com/r/music/post","content":"A sufficiently long forum post that must not become evidence for an artist biography.","score":0.8}
		]}`))
	}))
	defer server.Close()
	search, err := newTavilySearch(server.URL, keys)
	if err != nil {
		t.Fatal(err)
	}
	report, err := search.Search(context.Background(), ArtistSearchQuery{ArtistID: "artist", Name: "Artist", Albums: []string{"Album"}, Tracks: []string{"Track"}})
	if err != nil || len(report.Results) != 1 || report.Results[0].Domain != "artist.example" || keys.success != 1 {
		t.Fatalf("report=%#v keys=%#v err=%v", report, keys, err)
	}
}

func TestTavilySearchReturnsSafeHTTPReason(t *testing.T) {
	keys := &fakeSearchKeys{key: &keypool.LeasedKey{ID: 7, PlainKey: "invalid-key"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"must never be logged"}`))
	}))
	defer server.Close()
	search, err := newTavilySearch(server.URL, keys)
	if err != nil {
		t.Fatal(err)
	}
	_, err = search.Search(context.Background(), ArtistSearchQuery{ArtistID: "artist", Name: "Artist"})
	if err == nil || safePipelineErrorReason(err) != "tavily_http_401" || strings.Contains(err.Error(), "must never be logged") || keys.invalid != 1 {
		t.Fatalf("unexpected safe error: %v reason=%q keys=%#v", err, safePipelineErrorReason(err), keys)
	}
}

func TestArtistSearchQueryIsStableAndBounded(t *testing.T) {
	input := ArtistSearchQuery{ArtistID: "artist", Name: "Artist", Aliases: []string{"Alias B", "Alias A"}, Albums: []string{strings.Repeat("A", 256)}, Tracks: []string{"Track"}}
	first, version1, err := buildArtistSearchQuery(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Aliases = []string{"Alias A", "Alias B"}
	second, version2, err := buildArtistSearchQuery(input)
	if err != nil || first != second || version1 != version2 || len(first) > maxSearchIdentityBytes {
		t.Fatalf("query is not stable/bounded: %q %q %q %q err=%v", first, second, version1, version2, err)
	}
}

func TestWebSearchCacheExpiresAndForceRebuildInvalidates(t *testing.T) {
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store, err := NewStore(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PutIdentity(ctx, ArtistIdentity{ArtistID: "artist", ArtistName: "Artist", Status: ResolutionResolved, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	result := WebSearchResult{ID: "tavily:0", Title: "Official", URL: "https://artist.example/bio", Domain: "artist.example", Snippet: strings.Repeat("Factual artist biography snippet. ", 3), Score: 0.9}
	now := time.Now()
	if err := store.PutWebSearchCache(ctx, "artist", "version", []WebSearchResult{result}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if cached, found, err := store.WebSearchCache(ctx, "artist", "version", now); err != nil || !found || len(cached.Results) != 1 {
		t.Fatalf("cached=%#v found=%v err=%v", cached, found, err)
	}
	if _, found, err := store.WebSearchCache(ctx, "artist", "version", now.Add(2*time.Hour)); err != nil || found {
		t.Fatalf("expired cache found=%v err=%v", found, err)
	}
	if _, admitted, err := store.QueueJob(ctx, "artist", "Artist", true, now); err != nil || !admitted {
		t.Fatalf("force queue admitted=%v err=%v", admitted, err)
	}
	if _, found, err := store.WebSearchCache(ctx, "artist", "version", now); err != nil || found {
		t.Fatalf("force rebuild retained cache: found=%v err=%v", found, err)
	}
}

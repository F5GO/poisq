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
	var depths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || r.Header.Get("Authorization") != "Bearer secret-tavily-key" || request["api_key"] != nil || request["include_raw_content"] != false {
			t.Fatalf("unsafe request payload: %#v err=%v", request, err)
		}
		depth, _ := request["search_depth"].(string)
		if depth != "basic" && depth != "advanced" {
			t.Fatalf("unexpected search_depth: %q", depth)
		}
		depths = append(depths, depth)
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
	if len(depths) != 2 || depths[0] != "basic" || depths[1] != "advanced" {
		t.Fatalf("expected basic then advanced fallback, got depths=%v", depths)
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

func TestRussianSearchQueryAdaptation(t *testing.T) {
	ruByCatalog, _, err := buildArtistSearchQuery(ArtistSearchQuery{ArtistID: "1", Name: "Biicla", PreferRussian: true})
	if err != nil || !strings.Contains(ruByCatalog, "музыкант биография группа дискография") {
		t.Fatalf("expected Russian query for PreferRussian, got %q err=%v", ruByCatalog, err)
	}
	ruByCyrillic, _, err := buildArtistSearchQuery(ArtistSearchQuery{ArtistID: "2", Name: "Кино"})
	if err != nil || !strings.Contains(ruByCyrillic, "музыкант биография группа дискография") {
		t.Fatalf("expected Russian query for Cyrillic name, got %q err=%v", ruByCyrillic, err)
	}
	enQuery, _, err := buildArtistSearchQuery(ArtistSearchQuery{ArtistID: "3", Name: "Daft Punk"})
	if err != nil || !strings.Contains(enQuery, "music artist biography discography") {
		t.Fatalf("expected English query, got %q err=%v", enQuery, err)
	}
}

func TestArtistBiographyQueryStaysBroadWithReleaseHint(t *testing.T) {
	input := ArtistSearchQuery{
		ArtistID:           "izhevski",
		Name:               "Izhevski",
		IncludeReleaseHint: true,
		Tracks:             []string{"Лучший город на земле (ремикс)"},
	}
	bioQuery, _, err := buildArtistSearchQuery(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bioQuery, "Лучший город на земле") {
		t.Fatalf("expected primary biography query to stay broad without track title, got %q", bioQuery)
	}
	relQuery := buildArtistReleaseQuery(input)
	if !strings.Contains(relQuery, "Izhevski") || !strings.Contains(relQuery, "Лучший город на земле") {
		t.Fatalf("expected secondary release query to include artist and track, got %q", relQuery)
	}
	cyrillicTrackQuery, _, err := buildArtistSearchQuery(ArtistSearchQuery{
		ArtistID: "4atty",
		Name:     "4atty aka Tilla",
		Tracks:   []string{"Юрфак"},
	})
	if err != nil || !strings.Contains(cyrillicTrackQuery, "музыкант биография группа дискография") {
		t.Fatalf("expected Russian query for artist with Cyrillic tracks, got %q err=%v", cyrillicTrackQuery, err)
	}
}

func TestMixedScriptArtistFiltersHomonymResults(t *testing.T) {
	input := ArtistSearchQuery{
		ArtistID:           "simptom",
		Name:               "Sимптом",
		IncludeReleaseHint: true,
		Albums:             []string{"STYLE IN MC"},
		Tracks:             []string{"Юрфак"},
	}
	query, _, err := buildArtistSearchQuery(input)
	if err != nil || !strings.Contains(query, `"Sимптом"`) {
		t.Fatalf("expected quoted mixed-script artist name in query, got %q err=%v", query, err)
	}
	filtered := filterResultsForArtist(input, []WebSearchResult{
		{ID: "1", Title: "Группа Симптом — биография", Domain: "other.example", Snippet: "Рок-группа Симптом была основана в другом городе."},
		{ID: "2", Title: "Sимптом (Гребз)", Domain: "genius.com", Snippet: "Sимптом — участник коллектива Grebz, записавший альбом."},
		{ID: "3", Title: "STYLE IN MC", Domain: "music.example", Snippet: "Совместный релиз с участием 4atty aka Tilla и других."},
	})
	if len(filtered) != 2 || filtered[0].ID != "2" || filtered[1].ID != "3" {
		t.Fatalf("expected only exact Sимптом or matching release results, got %#v", filtered)
	}
}

func TestMultiWordArtistFiltersHomonymNoise(t *testing.T) {
	input := ArtistSearchQuery{
		ArtistID: "4atty",
		Name:     "4atty aka Tilla",
		Albums:   []string{"Дом на колесах"},
	}
	noise := []WebSearchResult{
		{ID: "1", Title: "Tilla - 2026 Company Profile", Domain: "tracxn.com", Snippet: "Tilla is a technology startup based in Bangalore."},
		{ID: "2", Title: "Gurudwara Majnu ka Tilla", Domain: "mapquest.com", Snippet: "Historic site in Delhi."},
	}
	if got := filterResultsForArtist(input, noise); len(got) != 0 {
		t.Fatalf("expected pure homonym noise to be filtered out, got %#v", got)
	}
	mixed := append(noise, WebSearchResult{
		ID: "3", Title: "4atty aka Tilla — биография", Domain: "genius.com", Snippet: "Киевский рэпер, участник группы Грибы.",
	})
	if got := filterResultsForArtist(input, mixed); len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("expected exact artist match to survive, got %#v", got)
	}

	biiclaInput := ArtistSearchQuery{
		ArtistID: "biicla",
		Name:     "Biicla",
		Albums:   []string{"No Place"},
	}
	biiclaNoise := []WebSearchResult{
		{ID: "1", Title: "Музыкант David Crosby. Биография", Domain: "ru.musichearts.fm", Snippet: "Биография американского музыканта."},
		{ID: "2", Title: "Кравченко, Алексей (музыкант)", Domain: "ru.wikipedia.org", Snippet: "Российский актёр и музыкант."},
	}
	if got := filterResultsForArtist(biiclaInput, biiclaNoise); len(got) != 0 {
		t.Fatalf("expected unrelated generic musician pages to be filtered out for Biicla, got %#v", got)
	}

	nicInput := ArtistSearchQuery{
		ArtistID: "nicd",
		Name:     "Nic D",
		Albums:   []string{"Scoop"},
	}
	nicQuery, _, err := buildArtistSearchQuery(nicInput)
	if err != nil || !strings.Contains(nicQuery, `"Nic D"`) || strings.Contains(nicQuery, "Scoop") {
		t.Fatalf("expected quoted Nic D without unquoted Scoop in primary query, got %q err=%v", nicQuery, err)
	}
	nicNoise := []WebSearchResult{
		{ID: "1", Title: "Scoop", Domain: "tavily.com", Snippet: "Tavily Scoop search product page."},
		{ID: "2", Title: "Scoop - song by Someone Else", Domain: "open.spotify.com", Snippet: "Listen to Scoop on Spotify."},
		{ID: "3", Title: "Nic D - Biography & Songs", Domain: "genius.com", Snippet: "Nic D is an independent pop-rap artist."},
	}
	if got := filterResultsForArtist(nicInput, nicNoise); len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("expected single-word album Scoop noise to be filtered out for Nic D, got %#v", got)
	}
}

func TestEvidenceOriginForDomainCollapsesMirrorsAndCatalogHosts(t *testing.T) {
	cases := map[string]string{
		"ru.wikipedia.org":       "wikipedia",
		"en.m.wikipedia.org":     "wikipedia",
		"wikipedia.akarpov.ru":   "wikipedia",
		"www.wikiwand.com":       "wikipedia",
		"wiki2.org":              "wikipedia",
		"music.yandex.ru":        "yandex_music",
		"music.yandex.com":       "yandex_music",
		"www.deezer.com":         "deezer",
		"music.apple.com":        "apple_music",
		"www.last.fm":            "lastfm",
		"www.discogs.com":        "discogs",
		"www.weekend.rambler.ru": "weekend.rambler.ru",
		"m.youtube.com":          "youtube.com",
		"wikiwandering.example":  "wikiwandering.example",
		"yandex.ru":              "yandex.ru",
	}
	for domain, want := range cases {
		if got := evidenceOriginForDomain(domain); got != want {
			t.Errorf("evidenceOriginForDomain(%q) = %q, want %q", domain, got, want)
		}
	}
}

func TestHasEnoughSearchResultsIgnoresWikipediaMirrors(t *testing.T) {
	snippet := strings.Repeat("Достаточно длинный фрагмент о творчестве исполнителя. ", 10)
	mirrored := []WebSearchResult{
		{Domain: "ru.wikipedia.org", Snippet: snippet},
		{Domain: "wikipedia.akarpov.ru", Snippet: snippet},
	}
	if hasEnoughSearchResults(mirrored) {
		t.Fatal("a Wikipedia article and its mirror must not count as two independent origins")
	}
	independent := append(mirrored[:1:1], WebSearchResult{Domain: "weekend.rambler.ru", Snippet: snippet})
	if !hasEnoughSearchResults(independent) {
		t.Fatal("two independent origins with enough text must be sufficient")
	}
}

package artistbio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/metadata"
)

func TestAppleMusicBiographySourceUsesReleaseEvidence(t *testing.T) {
	const secret = "apple-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Fatal("missing Apple token")
		}
		switch r.URL.Path {
		case "/v1/catalog/ru/search":
			_, _ = w.Write([]byte(`{"results":{"artists":{"data":[{"id":"10","attributes":{"name":"Кино"}}]}}}`))
		case "/v1/catalog/ru/artists/10/albums":
			_, _ = w.Write([]byte(`{"data":[{"attributes":{"name":"Группа крови","releaseDate":"1988-01-01"}},{"attributes":{"name":"Звезда по имени Солнце","releaseDate":"1989-01-01"}}]}`))
		case "/v1/catalog/ru/artists/10":
			_, _ = w.Write([]byte(`{"data":[{"id":"10","attributes":{"name":"Кино","url":"https://music.apple.com/ru/artist/kino/10","editorialNotes":{"standard":"Русское <b>описание</b>"}}}]}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	keys := &fakeBiographyKeys{key: &keypool.LeasedKey{ID: 1, PlainKey: secret}}
	source, err := newAppleMusicBiographySource(server.URL, keys)
	if err != nil {
		t.Fatal(err)
	}
	result, err := source.Fetch(context.Background(), yandexResolvedArtist())
	if err != nil || result == nil {
		t.Fatalf("fetch: result=%#v err=%v", result, err)
	}
	if result.SourceArtistID != "10" || result.OriginalText != "Русское описание" || result.LanguageHint != "ru" || keys.successes != 1 {
		t.Fatalf("unexpected result=%#v successes=%d", result, keys.successes)
	}
}

func TestDiscogsBiographySourceUsesReleaseEvidence(t *testing.T) {
	const secret = "discogs-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Discogs token="+secret {
			t.Fatal("missing Discogs token")
		}
		switch r.URL.Path {
		case "/database/search":
			_, _ = w.Write([]byte(`{"results":[{"id":20,"title":"Кино"}]}`))
		case "/artists/20":
			_, _ = w.Write([]byte(`{"id":20,"name":"Кино","profile":"Настоящий [b]профиль[/b]"}`))
		case "/artists/20/releases":
			_, _ = w.Write([]byte(`{"releases":[{"title":"Группа крови","year":1988},{"title":"Звезда по имени Солнце","year":1989}]}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	keys := &fakeBiographyKeys{key: &keypool.LeasedKey{ID: 2, PlainKey: secret}}
	source, err := newDiscogsBiographySource(server.URL, keys)
	if err != nil {
		t.Fatal(err)
	}
	result, err := source.Fetch(context.Background(), yandexResolvedArtist())
	if err != nil || result == nil {
		t.Fatalf("fetch: result=%#v err=%v", result, err)
	}
	if result.SourceArtistID != "20" || result.OriginalText != "Настоящий профиль" || keys.successes != 1 {
		t.Fatalf("unexpected result=%#v successes=%d", result, keys.successes)
	}
}

func TestCatalogBiographySourcesDisabledWithoutKeys(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	apple, _ := newAppleMusicBiographySource(server.URL, &fakeBiographyKeys{})
	discogs, _ := newDiscogsBiographySource(server.URL, &fakeBiographyKeys{})
	for _, source := range []BiographySource{apple, discogs} {
		result, err := source.Fetch(context.Background(), yandexResolvedArtist())
		if err != nil || result != nil {
			t.Fatalf("disabled source returned result=%#v err=%v", result, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("disabled sources called network: %d", calls.Load())
	}
}

func TestCatalogBiographySourceReportsInvalidKeyWithoutLeakingIt(t *testing.T) {
	const secret = "credential-marker"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(secret))
	}))
	defer server.Close()
	keys := &fakeBiographyKeys{key: &keypool.LeasedKey{ID: 3, PlainKey: secret}}
	source, _ := newAppleMusicBiographySource(server.URL, keys)
	_, err := source.Fetch(context.Background(), yandexResolvedArtist())
	if err == nil || strings.Contains(err.Error(), secret) || keys.invalid != 1 {
		t.Fatalf("unsafe auth failure: err=%v invalid=%d", err, keys.invalid)
	}
}

// guestOnlyResolvedArtist models an artist resolved by MusicBrainz whose local
// library context cannot confirm any catalog by releases (e.g. one guest track).
func guestOnlyResolvedArtist(links *metadata.MBArtistLinks) ResolvedArtist {
	artist := exactResolvedArtist()
	artist.Context.Tracks = []string{"Совместный трек"}
	artist.Identity.Confidence = 0.98
	artist.Links = links
	return artist
}

func TestCatalogSourcesUseExactMusicBrainzLinksWithoutSearch(t *testing.T) {
	links := &metadata.MBArtistLinks{ArtistMBID: exactTestMBID, DeezerID: "15170473", AppleMusicID: "1132504782", DiscogsID: "8097249", YandexMusicID: "777"}
	artist := guestOnlyResolvedArtist(links)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/ru/artists/1132504782":
			_, _ = w.Write([]byte(`{"data":[{"id":"1132504782","attributes":{"name":"Артист","editorialNotes":{"standard":"Описание Apple"}}}]}`))
		case "/artists/8097249":
			_, _ = w.Write([]byte(`{"id":8097249,"name":"Артист","profile":"Профиль Discogs"}`))
		case "/artists/777/brief-info":
			_, _ = w.Write([]byte(`{"result":{"artist":{"id":777,"description":{"text":"Описание Яндекса"}}}}`))
		case "/ru/artist/15170473":
			_, _ = w.Write([]byte(`<div id="naboo_artist_biography_resume_premium"><span itemprop="description">Описание Deezer</span></div>`))
		default:
			t.Errorf("unexpected search or path %q: exact links must skip name search", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	keys := &fakeBiographyKeys{key: &keypool.LeasedKey{ID: 3, PlainKey: "token"}}
	apple, _ := newAppleMusicBiographySource(server.URL, keys)
	discogs, _ := newDiscogsBiographySource(server.URL, keys)
	yandex, _ := newYandexBiographySource(server.URL, keys)
	deezer, _ := newDeezerBiographySource(server.URL, server.URL)
	cases := []struct {
		source BiographySource
		id     string
		text   string
	}{
		{apple, "1132504782", "Описание Apple"},
		{discogs, "8097249", "Профиль Discogs"},
		{yandex, "777", "Описание Яндекса"},
		{deezer, "15170473", "Описание Deezer"},
	}
	for _, tc := range cases {
		result, err := tc.source.Fetch(context.Background(), artist)
		if err != nil || result == nil {
			t.Fatalf("%s: result=%#v err=%v", tc.source.Provider(), result, err)
		}
		wantEvidence := []string{"id:musicbrainz", "link:" + string(tc.source.Provider())}
		if result.SourceArtistID != tc.id || result.OriginalText != tc.text || result.MatchConfidence != 0.98 || !slices.Equal(result.EvidenceCodes, wantEvidence) {
			t.Fatalf("%s: unexpected result %#v", tc.source.Provider(), result)
		}
	}
}

func TestExactCatalogIDIgnoresLinksOfAnotherArtistOrUnsafeIDs(t *testing.T) {
	artist := guestOnlyResolvedArtist(&metadata.MBArtistLinks{ArtistMBID: "00000000-0000-0000-0000-000000000000", DeezerID: "1"})
	if id := artist.exactCatalogID(ProviderDeezer); id != "" {
		t.Fatalf("links of another MusicBrainz artist were used: %q", id)
	}
	artist.Links = &metadata.MBArtistLinks{ArtistMBID: exactTestMBID, DeezerID: "../1"}
	if id := artist.exactCatalogID(ProviderDeezer); id != "" {
		t.Fatalf("unsafe catalog id accepted: %q", id)
	}
	artist.Links = nil
	if id := artist.exactCatalogID(ProviderDeezer); id != "" {
		t.Fatalf("id without links: %q", id)
	}
}

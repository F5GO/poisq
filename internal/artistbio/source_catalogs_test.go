package artistbio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/F5GO/poisq/internal/keypool"
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

package artistbio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/F5GO/poisq/internal/keypool"
)

func yandexResolvedArtist() ResolvedArtist {
	artist := exactResolvedArtist()
	artist.Context.Albums = []IdentityRelease{{Title: "Группа крови", Year: 1988}, {Title: "Звезда по имени Солнце", Year: 1989}}
	return artist
}

func TestYandexBiographySourceMatchesAlbumsBeforeBriefInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			_, _ = w.Write([]byte(`{"result":{"artists":{"results":[{"id":41075,"name":"КИНО"},{"id":999,"name":"Кино"}]}}}`))
		case "/artists/41075/direct-albums":
			_, _ = w.Write([]byte(`{"result":{"albums":[{"title":"Группа крови","year":1988},{"title":"Звезда по имени Солнце","year":1989}]}}`))
		case "/artists/999/direct-albums":
			_, _ = w.Write([]byte(`{"result":{"albums":[{"title":"Другой альбом","year":2020}]}}`))
		case "/artists/41075/brief-info":
			_, _ = w.Write([]byte(`{"result":{"artist":{"id":"41075","description":{"text":"Настоящее русское описание"}}}}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	source, err := newYandexBiographySource(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := source.Fetch(context.Background(), yandexResolvedArtist())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if result == nil || result.SourceArtistID != "41075" || result.OriginalText != "Настоящее русское описание" || result.MatchConfidence < AutoResolveConfidence {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestYandexBiographySourceRejectsNameOnlyMatch(t *testing.T) {
	var briefCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			_, _ = w.Write([]byte(`{"result":{"artists":{"results":[{"id":1,"name":"Кино"}]}}}`))
		case "/artists/1/direct-albums":
			_, _ = w.Write([]byte(`{"result":{"albums":[]}}`))
		case "/artists/1/brief-info":
			briefCalls.Add(1)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	source, _ := newYandexBiographySource(server.URL, nil)
	result, err := source.Fetch(context.Background(), yandexResolvedArtist())
	if err != nil || result != nil || briefCalls.Load() != 0 {
		t.Fatalf("name-only candidate fetched: result=%#v calls=%d err=%v", result, briefCalls.Load(), err)
	}
}

func TestYandexBiographySourceUsesTokenOnlyAfterPublicAuthFailure(t *testing.T) {
	const secret = "yandex-secret"
	var publicCalls, authenticatedCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			_, _ = w.Write([]byte(`{"result":{"artists":{"results":[{"id":1,"name":"Кино"}]}}}`))
		case "/artists/1/direct-albums":
			_, _ = w.Write([]byte(`{"result":{"albums":[{"title":"Группа крови","year":1988},{"title":"Звезда по имени Солнце","year":1989}]}}`))
		case "/artists/1/brief-info":
			if r.Header.Get("Authorization") == "" {
				publicCalls.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "OAuth "+secret {
				t.Fatal("unexpected authorization header")
			}
			authenticatedCalls.Add(1)
			_, _ = w.Write([]byte(`{"result":{"artist":{"id":1,"description":{"text":"Описание"}}}}`))
		}
	}))
	defer server.Close()
	keys := &fakeBiographyKeys{key: &keypool.LeasedKey{ID: 9, PlainKey: secret}}
	source, _ := newYandexBiographySource(server.URL, keys)
	result, err := source.Fetch(context.Background(), yandexResolvedArtist())
	if err != nil || result == nil {
		t.Fatalf("authenticated fallback: result=%#v err=%v", result, err)
	}
	if publicCalls.Load() != 1 || authenticatedCalls.Load() != 1 || keys.successes != 1 {
		t.Fatalf("unexpected calls public=%d auth=%d success=%d", publicCalls.Load(), authenticatedCalls.Load(), keys.successes)
	}
}

func TestYandexBiographySourceEmptyDescriptionReturnsNoCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			_, _ = w.Write([]byte(`{"result":{"artists":{"results":[{"id":1,"name":"Кино"}]}}}`))
		case "/artists/1/direct-albums":
			_, _ = w.Write([]byte(`{"result":{"albums":[{"title":"Группа крови"},{"title":"Звезда по имени Солнце"}]}}`))
		case "/artists/1/brief-info":
			_, _ = w.Write([]byte(`{"result":{"artist":{"id":1,"description":{"text":" "}}}}`))
		}
	}))
	defer server.Close()
	source, _ := newYandexBiographySource(server.URL, nil)
	result, err := source.Fetch(context.Background(), yandexResolvedArtist())
	if err != nil || result != nil {
		t.Fatalf("empty description: result=%#v err=%v", result, err)
	}
}

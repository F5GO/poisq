package artistbio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDeezerBiographySourceMatchesCatalogBeforePage(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/artist":
			_, _ = w.Write([]byte(`{"data":[{"id":4505880,"name":"Кино"},{"id":2,"name":"Кино"}]}`))
		case "/artist/4505880":
			_, _ = w.Write([]byte(`{"id":4505880,"name":"Кино"}`))
		case "/artist/2":
			_, _ = w.Write([]byte(`{"id":2,"name":"Кино"}`))
		case "/artist/4505880/albums":
			_, _ = w.Write([]byte(`{"data":[{"title":"Группа крови","release_date":"1988-01-01"},{"title":"Звезда по имени Солнце","release_date":"1989-01-01"}]}`))
		case "/artist/2/albums":
			_, _ = w.Write([]byte(`{"data":[{"title":"Другой альбом","release_date":"2020-01-01"}]}`))
		default:
			t.Fatalf("unexpected API path %q", r.URL.Path)
		}
	}))
	defer api.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ru/artist/4505880" || !strings.HasPrefix(r.Header.Get("Accept-Language"), "ru-RU") {
			t.Errorf("unexpected page request: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`<html><div id="naboo_artist_biography_resume_premium" class="quote"><span itemprop="description">Настоящее &laquo;описание&raquo;<br />Вторая строка</span></div></html>`))
	}))
	defer web.Close()
	source, err := newDeezerBiographySource(api.URL, web.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := source.Fetch(context.Background(), yandexResolvedArtist())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if result == nil || result.SourceArtistID != "4505880" || result.OriginalText != "Настоящее «описание»\nВторая строка" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestDeezerBiographySourceDoesNotFetchPageForNameOnlyMatch(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/artist":
			_, _ = w.Write([]byte(`{"data":[{"id":1,"name":"Кино"}]}`))
		case "/artist/1":
			_, _ = w.Write([]byte(`{"id":1,"name":"Кино"}`))
		case "/artist/1/albums":
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer api.Close()
	var webCalls atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { webCalls.Add(1) }))
	defer web.Close()
	source, _ := newDeezerBiographySource(api.URL, web.URL)
	result, err := source.Fetch(context.Background(), yandexResolvedArtist())
	if err != nil || result != nil || webCalls.Load() != 0 {
		t.Fatalf("name-only page fetch: result=%#v calls=%d err=%v", result, webCalls.Load(), err)
	}
}

func TestDeezerDescriptionExtractor(t *testing.T) {
	valid := []byte(`<div id="naboo_artist_biography_resume_premium" class="quote"><span itemprop="description">Первая<br>Вторая &amp; третья</span></div>`)
	text, err := extractDeezerDescription(valid)
	if err != nil || text != "Первая\nВторая & третья" {
		t.Fatalf("valid extract: %q %v", text, err)
	}
	invalid := map[string][]byte{
		"missing container": []byte(`<span itemprop="description">Описание</span>`),
		"reordered markers": []byte(`<span itemprop="description">Описание</span><div id="naboo_artist_biography_resume_premium">`),
		"script":            []byte(`<div id="naboo_artist_biography_resume_premium"><span itemprop="description"><script>alert(1)</script>Описание</span>`),
		"oversized":         []byte(`<div id="naboo_artist_biography_resume_premium"><span itemprop="description">` + strings.Repeat("x", maxDeezerDescriptionHTML+1) + `</span>`),
	}
	for name, body := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := extractDeezerDescription(body); err == nil {
				t.Fatal("unsafe fixture accepted")
			}
		})
	}
}

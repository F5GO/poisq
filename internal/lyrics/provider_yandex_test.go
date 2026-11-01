// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл provider_yandex_test.go тестирует работу адаптера Яндекс Музыки:
// наличие обязательного параметра page=0 в поисковом запросе и корректный парсинг /tracks/{id}/supplement.
package lyrics

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type yandexRoundTripFunc func(*http.Request) (*http.Response, error)

func (f yandexRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestYandexProvider_SearchRequiresPage0(t *testing.T) {
	provider := NewYandexProvider(2*time.Second, 4*time.Second)

	var interceptedSearchQuery string
	provider.client.Transport = yandexRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/search") {
			interceptedSearchQuery = r.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"result": {
						"tracks": {
							"results": [
								{
									"id": 12345,
									"title": "Отпускаю",
									"lyricsAvailable": true
								}
							]
						}
					}
				}`)),
			}, nil
		}

		if strings.Contains(r.URL.Path, "/supplement") {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"result": {
						"lyrics": {
							"fullLyrics": "Я не могу дышать...",
							"syncLyrics": [
								{"timestamp": 12000, "line": "Я не могу дышать...", "durationMs": 3000}
							]
						}
					}
				}`)),
			}, nil
		}

		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})

	res, err := provider.Fetch(context.Background(), SearchQuery{
		Artist: "МакСим",
		Title:  "Отпускаю",
	})

	if err != nil {
		t.Fatalf("Ошибка Fetch: %v", err)
	}

	if !strings.Contains(interceptedSearchQuery, "page=0") {
		t.Errorf("В поисковом запросе Яндекс Музыки отсутствует обязательный параметр page=0: %s", interceptedSearchQuery)
	}

	if res == nil || res.Kind != KindLRC {
		t.Fatalf("Ожидался синхронизированный LRC текст, получено: %+v", res)
	}

	if !strings.Contains(res.PlainLyrics, "Я не могу дышать") {
		t.Errorf("PlainLyrics не содержит ожидаемый текст: %s", res.PlainLyrics)
	}
}

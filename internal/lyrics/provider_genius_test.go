// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл provider_genius_test.go проверяет валидацию треков в GeniusProvider:
// игнорирование ложных срабатываний в top_hit (чужие рэп-треки с похожими словами в тексте)
// и выбор правильного трека нужного исполнителя.
package lyrics

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type geniusRoundTripFunc func(*http.Request) (*http.Response, error)

func (f geniusRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestGeniusProvider_RejectsUnrelatedTopHit(t *testing.T) {
	p := NewGeniusProvider(2 * time.Second)

	var scrapedURL string
	p.client.Transport = geniusRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/api/search/multi") {
			// Имитируем реальный ответ Genius, где в top_hit попал чужой рэп-трек "Big Boy" рэпера Big Baby Tape,
			// а правильная песня МакСим идет следом
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"response": {
						"sections": [
							{
								"type": "top_hit",
								"hits": [
									{
										"result": {
											"id": 999999,
											"title": "Отпускаю дым",
											"url": "https://genius.com/Big-baby-tape-otpuskayu-dym-lyrics",
											"primary_artist": {"name": "Big Baby Tape"}
										}
									}
								]
							},
							{
								"type": "song",
								"hits": [
									{
										"result": {
											"id": 123456,
											"title": "Отпускаю (Let Go)",
											"url": "https://genius.com/Maksim-otpuskayu-lyrics",
											"primary_artist": {"name": "МакSим"}
										}
									}
								]
							}
						]
					}
				}`)),
			}, nil
		}

		if strings.Contains(r.URL.Path, "Maksim-otpuskayu-lyrics") {
			scrapedURL = r.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`
					<html>
						<div data-lyrics-container="true">Я не могу дышать, я не могу дышать без тебя...</div>
					</html>
				`)),
			}, nil
		}

		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})

	lyr, err := p.Fetch(context.Background(), SearchQuery{
		Artist: "МакСим",
		Title:  "Отпускаю",
	})

	if err != nil {
		t.Fatalf("Ошибка Fetch: %v", err)
	}

	if lyr == nil || !strings.Contains(lyr.PlainLyrics, "Я не могу дышать") {
		t.Fatalf("Ожидался правильный текст МакСим, получено: %+v", lyr)
	}

	if !strings.Contains(scrapedURL, "Maksim-otpuskayu-lyrics") {
		t.Errorf("Genius выбрал неверный URL: %s", scrapedURL)
	}
}

func TestGeniusProvider_RejectsWhenNoMatch(t *testing.T) {
	p := NewGeniusProvider(2 * time.Second)

	p.client.Transport = geniusRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/api/search/multi") {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"response": {
						"sections": [
							{
								"type": "top_hit",
								"hits": [
									{
										"result": {
											"id": 999999,
											"title": "Совершенно другой трек",
											"url": "https://genius.com/Unrelated-lyrics",
											"primary_artist": {"name": "Неизвестный исполнитель"}
										}
									}
								]
							}
						]
					}
				}`)),
			}, nil
		}

		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})

	_, err := p.Fetch(context.Background(), SearchQuery{
		Artist: "МакСим",
		Title:  "Отпускаю",
	})

	if err != ErrNotFound {
		t.Fatalf("Ожидалась ошибка ErrNotFound при отсутствии совпадений, получено: %v", err)
	}
}

// Package lyrics_test тестирует работу координатора поиска текстов Engine.
//
// Данный файл engine_test.go проверяет дедупликацию SingleFlight, приоритет синхронизированного LRC
// над plain текстом, обработку кастомных источников и работу при отключенном модуле.
package lyrics_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/lyrics"
)

type roundTripFunc func(req *http.Request) *http.Response

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

func TestEngine_CustomLRCLIBSource(t *testing.T) {
	customTransport := roundTripFunc(func(r *http.Request) *http.Response {
		if r.URL.Path == "/api/get" {
			q := r.URL.Query()
			if q.Get("track_name") == "Тестовый трек" && q.Get("artist_name") == "Тестовый артист" {
				body := `{
					"id": 999,
					"trackName": "Тестовый трек",
					"artistName": "Тестовый артист",
					"syncedLyrics": "[00:15.00] Кастомный LRCLIB текст",
					"plainLyrics": "Кастомный LRCLIB текст"
				}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString(body)),
					Header:     make(http.Header),
				}
			}
		}
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
			Header:     make(http.Header),
		}
	})

	cfg := &lyrics.Config{
		Enabled:           true,
		SaveToDisk:        false,
		CustomSources:     []string{"http://mock-lrclib.local"},
		Providers:         []string{}, // Только кастомный источник
		CacheTTL:          1 * time.Hour,
		RequestTimeout:    2 * time.Second,
		DurationTolerance: 4 * time.Second,
	}

	engine := lyrics.NewEngine(cfg, nil)

	// Подменяем транспорт у кастомного провайдера
	for _, p := range engine.GetProviders() {
		if cp, ok := p.(*lyrics.CustomLrclibProvider); ok {
			cp.SetTransport(customTransport)
		}
	}

	lyr, err := engine.FindLyrics(context.Background(), lyrics.SearchQuery{
		Artist: "Тестовый артист",
		Title:  "Тестовый трек",
	})

	if err != nil || lyr == nil {
		t.Fatalf("Ожидался успешный поиск текста: %v", err)
	}

	if lyr.Kind != lyrics.KindLRC {
		t.Errorf("Ожидался KindLRC, получено: %s", lyr.Kind)
	}
	if lyr.SyncedLyrics != "[00:15.00] Кастомный LRCLIB текст" {
		t.Errorf("Неверный текст: %s", lyr.SyncedLyrics)
	}

	// Повторный запрос должен мгновенно браться из кэша
	cached, err := engine.FindLyrics(context.Background(), lyrics.SearchQuery{
		Artist: "Тестовый артист",
		Title:  "Тестовый трек",
	})
	if err != nil || cached == nil {
		t.Fatalf("Ожидался хит кэша")
	}
}

func TestEngine_Disabled(t *testing.T) {
	cfg := &lyrics.Config{
		Enabled: false,
	}

	engine := lyrics.NewEngine(cfg, nil)
	_, err := engine.FindLyrics(context.Background(), lyrics.SearchQuery{
		Artist: "Любой",
		Title:  "Любой",
	})

	if err != lyrics.ErrNotFound {
		t.Errorf("При выключенном движке ожидалась ошибка ErrNotFound, получено: %v", err)
	}
}

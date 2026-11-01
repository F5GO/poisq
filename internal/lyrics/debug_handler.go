// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл debug_handler.go реализует диагностические эндпоинты /_smart/lyrics/test и /_smart/lyrics/sources
// для проверки доступности источников, замера скорости поиска и отладки работы модуля.
package lyrics

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type providerTestResult struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Duration string `json:"duration"`
	Found    bool   `json:"found"`
	Kind     string `json:"kind,omitempty"`
	Preview  string `json:"preview,omitempty"`
	Error    string `json:"error,omitempty"`
}

type debugLyricsResponse struct {
	Query     SearchQuery          `json:"query"`
	CacheHit  bool                 `json:"cacheHit"`
	TotalTime string               `json:"totalTime"`
	Final     *Lyrics              `json:"final,omitempty"`
	Providers []providerTestResult `json:"providers,omitempty"`
}

// HandleLyricsDebugTest возвращает диагностический HTTP-обработчик для тестирования источников.
func HandleLyricsDebugTest(engine *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		q := r.URL.Query()

		artist := q.Get("artist")
		title := q.Get("title")
		if artist == "" || title == "" {
			http.Error(w, `{"error":"Параметры ?artist=... и ?title=... обязательны"}`, http.StatusBadRequest)
			return
		}

		searchQuery := SearchQuery{
			Artist: artist,
			Title:  title,
		}

		startTotal := time.Now()

		// 1. Проверяем кэш
		cached, hit := engine.cache.Get(artist, title)

		// 2. Тестируем каждого провайдера изолированно
		var provResults []providerTestResult
		for _, p := range engine.GetProviders() {
			pStart := time.Now()
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			lyr, err := p.Fetch(ctx, searchQuery)
			cancel()
			elapsed := time.Since(pStart)

			res := providerTestResult{
				ID:       p.ID(),
				Name:     p.Name(),
				Duration: elapsed.String(),
				Found:    err == nil && lyr != nil,
			}

			if err != nil {
				res.Error = err.Error()
			} else if lyr != nil {
				res.Kind = string(lyr.Kind)
				if lyr.SyncedLyrics != "" {
					lines := ParseLRC(lyr.SyncedLyrics)
					if len(lines) > 2 {
						res.Preview = lines[0].Text + " / " + lines[1].Text
					} else {
						res.Preview = lyr.SyncedLyrics
					}
				} else if lyr.PlainLyrics != "" {
					if len(lyr.PlainLyrics) > 100 {
						res.Preview = lyr.PlainLyrics[:100] + "..."
					} else {
						res.Preview = lyr.PlainLyrics
					}
				}
			}

			provResults = append(provResults, res)
		}

		// 3. Вызываем общий FindLyrics
		finalLyrics, _ := engine.FindLyrics(r.Context(), searchQuery)
		if cached != nil {
			finalLyrics = cached
		}

		resp := debugLyricsResponse{
			Query:     searchQuery,
			CacheHit:  hit,
			TotalTime: time.Since(startTotal).String(),
			Final:     finalLyrics,
			Providers: provResults,
		}

		_ = json.NewEncoder(w).Encode(resp)
	}
}

// HandleLyricsSourcesList возвращает список всех активных источников текстов песен.
func HandleLyricsSourcesList(engine *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		type sourceDTO struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}

		var list []sourceDTO
		for _, p := range engine.GetProviders() {
			list = append(list, sourceDTO{
				ID:   p.ID(),
				Name: p.Name(),
			})
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled": engine.cfg.Enabled,
			"sources": list,
		})
	}
}

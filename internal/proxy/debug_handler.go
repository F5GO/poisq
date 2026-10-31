// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл debug_handler.go реализует административно-диагностический эндпоинт
// GET /_smart/test?q=... для проверки трансформации запросов движком в браузере или через curl.
package proxy

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/F5GO/poisq/internal/config"
	"github.com/F5GO/poisq/internal/search"
)

// DebugResponse представляет JSON-структуру ответа диагностического эндпоинта.
type DebugResponse struct {
	Status         string              `json:"status"`
	Version        string              `json:"version"`
	RawQuery       string              `json:"raw_query"`
	RewrittenQuery string              `json:"rewritten_query"`
	AlternateQuery string              `json:"alternate_query,omitempty"`
	NeedsFanout    bool                `json:"needs_fanout"`
	DurationMicros int64               `json:"duration_micros"`
	Variants       []string            `json:"variants"`
	Matches        []search.MatchToken `json:"matched_tokens,omitempty"`
	TotalWords     int                 `json:"total_words_in_dictionary"`
}

// HandleDebugTest обрабатывает запросы к /_smart/test?q=...
func HandleDebugTest(engine *search.Engine, dict *search.Dictionary) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			q = strings.TrimSpace(r.URL.Query().Get("query"))
		}

		res := engine.ProcessQuery(q)

		resp := DebugResponse{
			Status:         "ok",
			Version:        config.Version,
			RawQuery:       res.RawQuery,
			RewrittenQuery: res.RewrittenQuery,
			AlternateQuery: res.AlternateQuery,
			NeedsFanout:    res.NeedsFanout,
			DurationMicros: res.DurationMicros,
			Variants:       res.Variants,
			Matches:        res.Matches,
			TotalWords:     dict.TotalWords(),
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

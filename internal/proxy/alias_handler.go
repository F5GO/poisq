// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл alias_handler.go реализует безопасный read-only просмотр текущих
// музыкальных алиасов (GET /_smart/aliases). Модификация через HTTP строго запрещена.
package proxy

import (
	"encoding/json"
	"net/http"

	"github.com/F5GO/poisq/internal/search"
)

// HandleAliasesAPI обрабатывает исключительно безопасные запросы просмотра алиасов (GET).
func HandleAliasesAPI(aliasMgr *search.AliasManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "Модификация через HTTP отключена в целях безопасности. Алиасы настраиваются через aliases.json",
			})
			return
		}

		if aliasMgr == nil {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "ok",
				"aliases": map[string]string{},
				"count":   0,
			})
			return
		}

		aliases := aliasMgr.GetAliases()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"count":   len(aliases),
			"aliases": aliases,
		})
	}
}

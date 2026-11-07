// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_keys.go обрабатывает управление пулом API-ключей (Gemini, Groq, Yandex):
// просмотр живых статусов ротации (Active, Cooldown 65s, Exhausted Daily до 11:00 МСК),
// добавление новых ключей на лету с шифрованием AES-256-GCM и удаление ключей.
package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

func (s *AdminServer) handleKeysAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		keys, err := s.keyPool.ListKeys()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"keys":       keys,
			"serverTime": time.Now().Unix(),
		})

	case http.MethodPost:
		var req struct {
			Provider string `json:"provider"`
			Label    string `json:"label"`
			APIKey   string `json:"apiKey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		id, err := s.keyPool.AddKey(req.Provider, req.Label, req.APIKey)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Если в очереди были задачи, ожидающие квоту — сразу будим воркера!
		s.worker.Wake()

		writeJSON(w, http.StatusCreated, map[string]any{
			"id":     id,
			"status": "added",
		})

	case http.MethodDelete:
		idStr := r.URL.Query().Get("id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid key id")
			return
		}
		if err := s.keyPool.DeleteKey(id); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

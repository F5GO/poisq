package admin

// Данный файл api_cast.go — настройки трансляции на колонки через Music Assistant (Studio → Настройки).
// GET возвращает настройки и то, что видно в MA (плееры, файловые источники); POST сохраняет настройки.
// Токен MA хранится в общем пуле ключей (провайдер music_assistant), сюда не попадает.

import (
	"encoding/json"
	"net/http"

	"github.com/F5GO/poisq/internal/cast"
)

// SetCast подключает сервис трансляции.
func (s *AdminServer) SetCast(c *cast.Service) { s.cast = c }

func (s *AdminServer) handleCast(w http.ResponseWriter, r *http.Request) {
	if s.cast == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "трансляция не подключена")
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var st cast.Settings
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := s.cast.SaveSettings(st); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	st, err := s.cast.Settings()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]any{
		"settings":  st,
		"hasToken":  s.keyPool.HasKeys(cast.TokenProvider),
		"players":   []cast.Player{},
		"providers": []cast.Provider{},
	}
	if players, providers, err := s.cast.Discover(r.Context()); err != nil {
		resp["error"] = err.Error()
	} else {
		resp["players"], resp["providers"] = players, providers
	}
	writeJSON(w, http.StatusOK, resp)
}

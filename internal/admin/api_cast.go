package admin

// Данный файл api_cast.go — настройки трансляции на колонки через Music Assistant (Studio → Настройки).
// GET возвращает настройки, статус токена и устройства/источники из MA;
// POST сохраняет настройки и при необходимости обновляет или удаляет токен доступа MA.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/F5GO/poisq/internal/cast"
)

// SetCast подключает сервис трансляции.
func (s *AdminServer) SetCast(c *cast.Service) { s.cast = c }

type castSaveRequest struct {
	URL        string   `json:"url"`
	Provider   string   `json:"provider"`
	Players    []string `json:"players"`
	Token      string   `json:"token,omitempty"`
	ClearToken bool     `json:"clearToken,omitempty"`
}

func (s *AdminServer) handleCast(w http.ResponseWriter, r *http.Request) {
	if s.cast == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "трансляция не подключена")
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var req castSaveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		cleanToken := strings.TrimSpace(req.Token)
		if req.ClearToken || cleanToken != "" {
			if keys, err := s.keyPool.ListKeys(); err == nil {
				for _, k := range keys {
					if k.Provider == cast.TokenProvider {
						_ = s.keyPool.DeleteKey(k.ID)
					}
				}
			}
		}
		if cleanToken != "" {
			if _, err := s.keyPool.AddKey(cast.TokenProvider, "Music Assistant", cleanToken); err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		st := cast.Settings{
			URL:      req.URL,
			Provider: req.Provider,
			Players:  req.Players,
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
	tokenMask := ""
	if keys, err := s.keyPool.ListKeys(); err == nil {
		for _, k := range keys {
			if k.Provider == cast.TokenProvider {
				tokenMask = k.KeyMask
				break
			}
		}
	}
	resp := map[string]any{
		"settings":  st,
		"hasToken":  s.keyPool.HasKeys(cast.TokenProvider),
		"tokenMask": tokenMask,
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

// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_staging.go реализует эндпоинты Staging-экрана рефакторинга:
// просмотр предложений от MusicBrainz, Mojibake Fixer и Cover Fetcher, а также
// применение правок [Применить], отклонение [Игнорировать] и откат [Откатить (Undo)].
package admin

import (
	"encoding/json"
	"net/http"
)

func (s *AdminServer) handleStagingList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	statusFilter := r.URL.Query().Get("status")
	if statusFilter == "" {
		statusFilter = "pending"
	}

	items, err := s.stagingRepo.List(statusFilter, 300)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
	})
}

func (s *AdminServer) handleStagingApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		ID  int64   `json:"id"`
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	targets := req.IDs
	if req.ID > 0 {
		targets = append(targets, req.ID)
	}
	if len(targets) == 0 {
		writeJSONError(w, http.StatusBadRequest, "не указан ID предложения")
		return
	}

	applied := 0
	var lastErr string
	for _, id := range targets {
		if err := s.stagingSvc.ApplyProposal(r.Context(), id); err != nil {
			lastErr = err.Error()
		} else {
			applied++
		}
	}

	if applied == 0 && lastErr != "" {
		writeJSONError(w, http.StatusBadRequest, lastErr)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"applied":   applied,
		"lastError": lastErr,
	})
}

func (s *AdminServer) handleStagingIgnore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid proposal id")
		return
	}

	if err := s.stagingSvc.IgnoreProposal(req.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "ignored"})
}

func (s *AdminServer) handleStagingUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid proposal id")
		return
	}

	if err := s.stagingSvc.UndoProposal(r.Context(), req.ID); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "reverted"})
}

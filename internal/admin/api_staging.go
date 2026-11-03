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

	"github.com/F5GO/poisq/internal/metadata"
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

	// Для предложений по обложкам проверяем актуальное состояние папки на диске,
	// чтобы гарантированно отображать существующую обложку cover.jpg, даже если в старом слепке стояло false.
	for i := range items {
		if items[i].ChangeType == "cover" && items[i].FolderPath != "" && s.guard != nil {
			var cur map[string]any
			if err := json.Unmarshal([]byte(items[i].CurrentJSON), &cur); err == nil {
				hasCov, _ := cur["hasCover"].(bool)
				existingFile, _ := cur["existingFile"].(string)
				if !hasCov || existingFile == "" {
					if safeFolder, err := s.guard.ValidateTarget(items[i].FolderPath, false); err == nil {
						if foundFile := metadata.FindLocalCoverFile(safeFolder); foundFile != "" {
							cur["hasCover"] = true
							cur["existingFile"] = foundFile
							dim := metadata.DetectImageDimensions(foundFile)
							cur["width"] = dim.Width
							cur["height"] = dim.Height
							if updatedBytes, err := json.Marshal(cur); err == nil {
								items[i].CurrentJSON = string(updatedBytes)
							}
						}
					}
				}
			}
		}
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
		ID       int64   `json:"id"`
		IDs      []int64 `json:"ids"`
		CoverURL string  `json:"coverUrl,omitempty"`
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
		if err := s.stagingSvc.ApplyProposalWithCustomCover(r.Context(), id, req.CoverURL); err != nil {
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

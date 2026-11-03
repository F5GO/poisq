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
	"strings"

	"github.com/F5GO/poisq/internal/db"
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

	// Для предложений по обложкам:
	// 1. Проверяем актуальное состояние папки на диске (надежный поиск с нормализацией дефисов и родительских папок).
	// 2. Очищаем от старых караоке/трибьют обложек, заменяя их на подлинные релизы исполнителя.
	var cleanItems []db.StagingChangeRecord
	for _, item := range items {
		if item.ChangeType == "cover" && item.FolderPath != "" {
			// 1. Проверка локальной обложки на диске (или встроенной в теги аудиофайла)
			var cur map[string]any
			if err := json.Unmarshal([]byte(item.CurrentJSON), &cur); err == nil {
				hasCov, _ := cur["hasCover"].(bool)
				existingFile, _ := cur["existingFile"].(string)
				root := ""
				if s.guard != nil {
					root = s.guard.Root()
				}
				if !hasCov || existingFile == "" {
					if foundFile := metadata.FindCoverAnywhere(root, item.FolderPath); foundFile != "" {
						cur["hasCover"] = true
						cur["existingFile"] = foundFile
						dim := metadata.DetectImageDimensions(foundFile)
						cur["width"] = dim.Width
						cur["height"] = dim.Height
						if updatedBytes, err := json.Marshal(cur); err == nil {
							item.CurrentJSON = string(updatedBytes)
							_ = s.stagingRepo.UpdateCurrentJSON(item.ID, string(updatedBytes))
						}
					} else {
						targetDir := metadata.ResolveFuzzyDirectory(root, item.FolderPath)
						if targetDir == "" {
							targetDir = item.FolderPath
						}
						if imgBytes, _ := metadata.ExtractCoverFromAudioOrDir(targetDir); len(imgBytes) > 0 {
							cur["hasCover"] = true
							cur["existingFile"] = targetDir
							if updatedBytes, err := json.Marshal(cur); err == nil {
								item.CurrentJSON = string(updatedBytes)
								_ = s.stagingRepo.UpdateCurrentJSON(item.ID, string(updatedBytes))
							}
						}
					}
				}
			}

			// 2. Проверка предлагаемой обложки на караоке / трибьюты
			var prop map[string]any
			if err := json.Unmarshal([]byte(item.ProposedJSON), &prop); err == nil {
				propURL, _ := prop["coverUrl"].(string)
				propArtist, _ := prop["artist"].(string)
				propAlbum, _ := prop["album"].(string)

				isDisallowed := metadata.IsDisallowedCoverRelease(propArtist, propAlbum, propArtist, propAlbum)
				if !isDisallowed {
					lowerURL := strings.ToLower(propURL)
					for _, kw := range metadata.DisallowedCoverKeywords {
						if strings.Contains(lowerURL, kw) {
							isDisallowed = true
							break
						}
					}
				}

				if isDisallowed || propURL == "" {
					// Ищем подлинные варианты через CoverFetcher
					fetcher := metadata.NewCoverFetcher(s.guard)
					cands := fetcher.FindCoverCandidates(r.Context(), "", propArtist, propAlbum, 8)
					if len(cands) > 0 {
						best := cands[0]
						prop["coverUrl"] = best.URL
						prop["source"] = best.Source
						prop["width"] = best.Width
						prop["height"] = best.Height
						prop["alternatives"] = best.Alternatives
						prop["reason"] = "Обложка высокого разрешения (" + best.Source + ")"
						if upBytes, err := json.Marshal(prop); err == nil {
							item.ProposedJSON = string(upBytes)
							_ = s.stagingRepo.UpdateProposedJSON(item.ID, string(upBytes))
						}
					} else {
						// Если подлинных обложек нет, исключаем это некорректное предложение
						_ = s.stagingRepo.MarkStatus(item.ID, "ignored")
						continue
					}
				}
			}
		}
		cleanItems = append(cleanItems, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": cleanItems,
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

// handleStagingClearCovers сбрасывает все незавершенные предложения обложек в Staging,
// позволяя запустить повторный аудит обложек с чистого листа.
func (s *AdminServer) handleStagingClearCovers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	deleted, err := s.stagingRepo.ClearPendingCoverProposals()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "cleared",
		"deleted": deleted,
		"message": "Очередь предложений обложек успешно очищена",
	})
}

// handleStagingClearAll полностью очищает очередь pending-предложений в Staging.
func (s *AdminServer) handleStagingClearAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	deleted, err := s.stagingRepo.ClearAllPending()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "cleared",
		"deleted": deleted,
		"message": "Вся очередь Staging очищена",
	})
}

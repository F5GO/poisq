// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_lyrics.go реализует эндпоинты Менеджера LRC: фильтрацию треков,
// мгновенное переключение между сохраненными ответами 5 провайдеров из provider_cache,
// ручную правку таймингов (с атомарной записью .lrc рядом с треком) и постановку задач в очередь.
package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/F5GO/poisq/internal/logger"
	"github.com/F5GO/poisq/internal/lyrics"
)

func (s *AdminServer) handleTracksList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	status := r.URL.Query().Get("status")
	search := r.URL.Query().Get("q")

	tracks, err := s.tracksRepo.ListTracks(status, search, 200)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"tracks": tracks})
}

func (s *AdminServer) handleLyricsCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	trackID := strings.TrimSpace(r.URL.Query().Get("trackId"))
	if trackID == "" {
		writeJSONError(w, http.StatusBadRequest, "trackId is required")
		return
	}

	track, err := s.tracksRepo.GetByID(trackID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "track not found")
		return
	}

	cached, err := s.cacheRepo.ListByTrack(trackID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"track":      track,
		"candidates": cached,
	})
}

func (s *AdminServer) handleLyricsSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		TrackID  string `json:"trackId"`
		Source   string `json:"source"`
		Content  string `json:"content"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	track, err := s.tracksRepo.GetByID(req.TrackID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "track not found")
		return
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeJSONError(w, http.StatusBadRequest, "текст не может быть пустым")
		return
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "manual_edit"
	}

	status := "plain"
	if req.Verified {
		status = "verified"
	} else if len(lyrics.ParseLRC(content)) > 0 {
		status = "synced"
	}

	// Атомарно сохраняем .lrc файл прямо рядом с аудиофайлом, чтобы Navidrome сразу его подтянул
	lrcWritten := false
	lrcPath, writeErr := s.lrcWriter.WriteForAudioFile(track.FilePath, content)
	if writeErr == nil {
		lrcWritten = true
	}

	if err := s.tracksRepo.UpdateLyrics(track.ID, status, source, content, track.LangCode, lrcWritten); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     status,
		"lrcWritten": lrcWritten,
		"lrcPath":    lrcPath,
		"writeError": errString(writeErr),
	})
}

func (s *AdminServer) handleLyricsEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		TrackID    string `json:"trackId"`
		JobType    string `json:"jobType"` // "tournament", "ai_consensus", "whisper_karaoke", "full_pipeline"
		EnqueueAll bool   `json:"enqueueAll"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.JobType == "" {
		req.JobType = "full_pipeline"
	}

	if req.JobType == "whisper_karaoke" && (s.keyPool == nil || !s.keyPool.HasKeys("groq")) {
		writeJSONError(w, http.StatusBadRequest, "Для генерации караоке через Whisper требуется API-ключ Groq Cloud. Добавьте его во вкладке «Ключи API». Для обычного поиска нажмите кнопку «Найти текст».")
		return
	}

	logger.Info("[API] Постановка задачи в очередь: trackId=%s, jobType=%s, enqueueAll=%v", req.TrackID, req.JobType, req.EnqueueAll)

	enqueued := 0
	if req.EnqueueAll {
		missing, err := s.tracksRepo.ListTracks("none", "", 500)
		if err == nil {
			for _, t := range missing {
				if err := s.jobRepo.Enqueue(t.ID, req.JobType, 1); err == nil {
					enqueued++
				}
			}
		}
	} else if strings.TrimSpace(req.TrackID) != "" {
		if err := s.jobRepo.Enqueue(strings.TrimSpace(req.TrackID), req.JobType, 10); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		enqueued = 1
	}

	s.worker.Wake()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "queued",
		"enqueued": enqueued,
	})
}

func (s *AdminServer) handleJobsList(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		idStr := r.URL.Query().Get("id")
		if idStr != "" {
			if id, err := strconv.ParseInt(idStr, 10, 64); err == nil && id > 0 {
				_ = s.jobRepo.DeleteJob(id)
			}
		}
	} else if r.Method == http.MethodPost {
		var req struct {
			Paused bool `json:"paused"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			s.worker.SetPaused(req.Paused)
		}
	}

	jobs, err := s.jobRepo.ListRecent(100)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":   jobs,
		"paused": s.worker.IsPaused(),
	})
}

// handleJobAction обрабатывает индивидуальные команды над задачами очереди:
// "pause" — приостановить конкретную задачу;
// "resume" — возобновить конкретную задачу;
// "retry" — сбросить счетчики и перезапустить задачу;
// "delete" — удалить задачу из очереди.
func (s *AdminServer) handleJobAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "Разрешены только POST и DELETE")
		return
	}

	var req struct {
		ID     int64  `json:"id"`
		Action string `json:"action"` // "pause", "resume", "retry", "delete"
	}

	if r.Method == http.MethodDelete {
		idStr := r.URL.Query().Get("id")
		if idStr != "" {
			req.ID, _ = strconv.ParseInt(idStr, 10, 64)
			req.Action = "delete"
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Неверный формат JSON запроса")
			return
		}
	}

	if req.ID <= 0 {
		writeJSONError(w, http.StatusBadRequest, "Не указан корректный ID задачи")
		return
	}

	var err error
	switch req.Action {
	case "pause":
		err = s.jobRepo.PauseJob(req.ID)
		logger.Info("[API] Задача #%d приостановлена пользователем", req.ID)
	case "resume":
		err = s.jobRepo.ResumeJob(req.ID)
		if err == nil && s.worker != nil {
			s.worker.Wake()
		}
		logger.Info("[API] Задача #%d возобновлена пользователем", req.ID)
	case "retry":
		err = s.jobRepo.RetryJob(req.ID)
		if err == nil && s.worker != nil {
			s.worker.Wake()
		}
		logger.Info("[API] Задача #%d перезапущена пользователем", req.ID)
	case "delete":
		err = s.jobRepo.DeleteJob(req.ID)
		logger.Info("[API] Задача #%d удалена пользователем", req.ID)
	default:
		writeJSONError(w, http.StatusBadRequest, "Неизвестное действие: "+req.Action)
		return
	}

	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Ошибка выполнения действия: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"id":     req.ID,
		"action": req.Action,
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

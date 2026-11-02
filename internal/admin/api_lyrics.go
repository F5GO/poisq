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
	"strings"

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
	if r.Method == http.MethodPost {
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

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

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

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/logger"
	"github.com/F5GO/poisq/internal/lyrics"
	"github.com/F5GO/poisq/internal/search"
)

func (s *AdminServer) handleTracksList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	status := r.URL.Query().Get("status")
	search := r.URL.Query().Get("q")

	var tracks []db.TrackRecord
	var err error
	if strings.TrimSpace(search) == "" {
		tracks, err = s.tracksRepo.ListTracks(status, "", 200)
	} else {
		tracks, err = s.smartSearchTracks(status, search, 200)
	}
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

func (s *AdminServer) handleLyricsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		TrackID string `json:"trackId"`
	}
	if r.Method == http.MethodDelete {
		req.TrackID = strings.TrimSpace(r.URL.Query().Get("trackId"))
	} else {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	trackID := strings.TrimSpace(req.TrackID)
	if trackID == "" {
		writeJSONError(w, http.StatusBadRequest, "trackId is required")
		return
	}

	track, err := s.tracksRepo.GetByID(trackID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "track not found")
		return
	}

	var lrcDeletedPath string
	var delErr error
	if s.lrcWriter != nil && track.FilePath != "" {
		lrcDeletedPath, delErr = s.lrcWriter.DeleteForAudioFile(track.FilePath)
	}

	if err := s.tracksRepo.ClearLyrics(track.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.cacheRepo.DeleteAllByTrack(track.ID)

	logger.Info("[API] Удален текст и файл .lrc для трека «%s — %s» (%s)", track.Artist, track.Title, track.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"status":     "none",
		"lrcDeleted": lrcDeletedPath,
		"deleteErr":  errString(delErr),
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

	jobLogs := make(map[int64][]string, len(jobs))
	if s.worker != nil {
		for _, j := range jobs {
			if logs := s.worker.GetJobLogs(j.ID); len(logs) > 0 {
				jobLogs[j.ID] = logs
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":    jobs,
		"jobLogs": jobLogs,
		"paused":  s.worker.IsPaused(),
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
		Stage  string `json:"stage"`  // "resume" (с последнего этапа) или "restart" (заново с 1 этапа)
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
		if req.Stage == "restart" {
			if jobRec, jErr := s.jobRepo.GetByID(req.ID); jErr == nil && jobRec != nil {
				_ = s.cacheRepo.Delete(jobRec.TrackID, "whisper_raw")
				_ = s.cacheRepo.Delete(jobRec.TrackID, "whisper_words_v2")
				_ = s.cacheRepo.Delete(jobRec.TrackID, "whisper_words_v3")
				logger.Info("[API] Задача #%d: очищен промежуточный кэш Whisper для трека %s (перезапуск с 1 этапа)", req.ID, jobRec.TrackID)
			}
			if s.worker != nil {
				s.worker.ClearJobLogs(req.ID)
			}
		}
		err = s.jobRepo.RetryJob(req.ID)
		if err == nil && s.worker != nil {
			s.worker.Wake()
		}
		logger.Info("[API] Задача #%d перезапущена пользователем (режим этапа: %s)", req.ID, req.Stage)
	case "delete":
		err = s.jobRepo.DeleteJob(req.ID)
		if s.worker != nil {
			s.worker.ClearJobLogs(req.ID)
		}
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

// smartSearchTracks ищет по библиотеке с учётом регистра, смешанных алфавитов («МакSим»), транслитерации,
// смены раскладки и мелких опечаток (см. internal/search/library_match.go).
func (s *AdminServer) smartSearchTracks(status, query string, limit int) ([]db.TrackRecord, error) {
	rows, err := s.tracksRepo.ListSearchRows(status)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(rows))
	keys := make([]search.LibraryKey, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
		keys[i] = search.BuildLibraryKey(r.Artist, r.Title, r.Album, r.FilePath)
	}
	ranked := search.RankByQuery(search.PrepareLibraryQuery(query), ids, keys, limit)
	order := make([]string, len(ranked))
	for i, r := range ranked {
		order[i] = r.ID
	}
	logger.Info("[STUDIO-SEARCH] запрос %q: найдено %d из %d треков", query, len(ranked), len(rows))
	return s.tracksRepo.ListByIDs(order)
}

// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_health.go обрабатывает запросы Дашборда здоровья медиатеки (/api/v1/health)
// и запуск синхронизации с Navidrome или глубокого аудита папок (MusicBrainz / Cover Art).
package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/F5GO/poisq/internal/logger"
)

func (s *AdminServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	stats, err := s.tracksRepo.GetHealthStats()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"stats":        stats,
		"workerPaused": s.worker.IsPaused(),
		"serverTime":   time.Now().Unix(),
	})
}

func (s *AdminServer) handleScanSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var navDB *sql.DB
	if s.openNavDB != nil {
		var err error
		navDB, err = s.openNavDB()
		if err == nil && navDB != nil {
			defer navDB.Close()
		}
	}

	synced, err := s.scanner.SyncFromNavidromeDB(navDB)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"syncedTracks": synced,
	})
}

func (s *AdminServer) handleScanAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		LimitFolders int `json:"limitFolders"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.LimitFolders <= 0 {
		req.LimitFolders = 20
	}

	go func(limit int) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		logger.Info("[AUDIT] Запуск глубокого аудита для %d папок...", limit)
		created, err := s.scanner.RunDeepAudit(ctx, limit)
		if err != nil {
			logger.Warn("[AUDIT] Ошибка при выполнении аудита: %v", err)
		} else {
			logger.Info("[AUDIT] Глубокий аудит завершен. Создано новых предложений: %d", created)
		}
	}(req.LimitFolders)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "started",
		"message": "Глубокий аудит MusicBrainz и обложек запущен в фоне (Staging-режим)",
	})
}

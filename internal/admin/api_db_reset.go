// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_db_reset.go реализует эндпоинт полного сброса базы данных poisq.db
// (/api/v1/db/reset) с автоматическим запуском повторной синхронизации каталога из Navidrome.
package admin

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/F5GO/poisq/internal/db"
)

// SetPoisqDB подключает дескриптор базы данных poisq.db для операций полного сброса и обслуживания.
func (s *AdminServer) SetPoisqDB(poisqDB *sql.DB) {
	s.poisqDB = poisqDB
}

// handleDatabaseReset выполняет полный сброс базы данных poisq.db (очистку очереди Staging,
// каталога треков и кэша текстов), сохраняя по умолчанию API-ключи, и сразу запускает
// свежую синхронизацию медиатеки из Navidrome.
func (s *AdminServer) handleDatabaseReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if s.poisqDB == nil {
		writeJSONError(w, http.StatusInternalServerError, "poisq database is not configured")
		return
	}

	var req struct {
		KeepKeys *bool `json:"keepKeys"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	keepKeys := true
	if req.KeepKeys != nil {
		keepKeys = *req.KeepKeys
	}

	if err := db.ResetDatabase(s.poisqDB, keepKeys); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "ошибка сброса базы данных: "+err.Error())
		return
	}

	// Запускаем немедленную синхронизацию каталога из Navidrome
	var syncedCount int
	if s.scanner != nil && s.openNavDB != nil {
		if navDB, err := s.openNavDB(); err == nil && navDB != nil {
			defer navDB.Close()
			syncedCount, _ = s.scanner.SyncFromNavidromeDB(navDB)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"message":      "База данных успешно сброшена, выполнена свежая синхронизация с Navidrome",
		"syncedTracks": syncedCount,
		"keysRetained": keepKeys,
	})
}

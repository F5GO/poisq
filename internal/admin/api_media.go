// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_media.go реализует безопасную отдачу аудиопотоков (/api/v1/tracks/stream)
// и локальных обложек альбомов (/api/v1/covers/local) для караоке-плеера и предпросмотра в Staging.
package admin

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/F5GO/poisq/internal/metadata"
)

// handleTrackStream безопасно отдает аудиофайл браузеру с поддержкой Range-запросов
// для воспроизведения в караоке-плеере.
func (s *AdminServer) handleTrackStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	trackID := strings.TrimSpace(r.URL.Query().Get("id"))
	if trackID == "" {
		writeJSONError(w, http.StatusBadRequest, "track id is required")
		return
	}

	track, err := s.tracksRepo.GetByID(trackID)
	if err != nil || track == nil {
		writeJSONError(w, http.StatusNotFound, "track not found")
		return
	}

	if s.guard == nil {
		writeJSONError(w, http.StatusInternalServerError, "path guard is not configured")
		return
	}

	safePath, err := s.guard.ValidateTarget(track.FilePath, false)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, "access denied: "+err.Error())
		return
	}

	http.ServeFile(w, r, safePath)
}

// handleLocalCover безопасно отдает локальный файл обложки для предпросмотра и сравнения в Staging.
func (s *AdminServer) handleLocalCover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	coverPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if coverPath == "" {
		writeJSONError(w, http.StatusBadRequest, "cover path is required")
		return
	}

	if s.guard == nil {
		writeJSONError(w, http.StatusInternalServerError, "path guard is not configured")
		return
	}

	safePath, err := s.guard.ValidateTarget(coverPath, false)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, "access denied: "+err.Error())
		return
	}

	// Если передан путь к папке альбома, автоматически ищем файл обложки внутри
	if fi, err := os.Stat(safePath); err == nil && fi.IsDir() {
		found := metadata.FindLocalCoverFile(safePath)
		if found != "" {
			http.ServeFile(w, r, found)
			return
		}
		writeJSONError(w, http.StatusNotFound, "cover not found in folder")
		return
	} else if err != nil {
		// Если файла по указанному пути нет, проверяем папку на наличие других обложек (folder.jpg, cover.png)
		dir := filepath.Dir(safePath)
		if found := metadata.FindLocalCoverFile(dir); found != "" {
			http.ServeFile(w, r, found)
			return
		}
		writeJSONError(w, http.StatusNotFound, "cover file not found")
		return
	}

	http.ServeFile(w, r, safePath)
}

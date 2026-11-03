// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_covers.go реализует эндпоинт поиска альтернативных вариантов обложек
// (/api/v1/covers/candidates), позволяя пользователю выбирать наилучшую обложку в Staging.
package admin

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/F5GO/poisq/internal/metadata"
)

// handleCoverCandidates ищет доступные варианты обложек в Cover Art Archive и Apple Music / iTunes
// со строгой фильтрацией караоке и трибьютов.
func (s *AdminServer) handleCoverCandidates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	album := strings.TrimSpace(r.URL.Query().Get("album"))
	mbid := strings.TrimSpace(r.URL.Query().Get("mbid"))
	folderPath := strings.TrimSpace(r.URL.Query().Get("path"))

	// Если артист или альбом не переданы явно, извлекаем их из структуры пути папки
	if (artist == "" || album == "") && folderPath != "" {
		clean := filepath.Clean(folderPath)
		baseName := filepath.Base(clean)
		parentName := filepath.Base(filepath.Dir(clean))

		if artist == "" && parentName != "." && parentName != "/" {
			artist = parentName
		}
		if album == "" && baseName != "." && baseName != "/" {
			album = baseName
		}
	}

	album = metadata.CleanTargetAlbum(artist, album)

	if artist == "" && album == "" && mbid == "" {
		writeJSONError(w, http.StatusBadRequest, "необходимо указать artist или album")
		return
	}

	fetcher := metadata.NewCoverFetcher(s.guard)
	candidates := fetcher.FindCoverCandidates(r.Context(), mbid, artist, album, 12)

	writeJSON(w, http.StatusOK, map[string]any{
		"artist":     artist,
		"album":      album,
		"count":      len(candidates),
		"candidates": candidates,
	})
}

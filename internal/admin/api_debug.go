// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_debug.go реализует диагностический эндпоинт (/api/v1/debug/fs)
// для моментальной проверки файловой системы, точек монтирования томов Docker
// и соответствия путей между базой данных Navidrome и реальным диском.
package admin

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/F5GO/poisq/internal/metadata"
)

type fsDebugItem struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size,omitempty"`
}

// handleDebugFS возвращает дерево файлов медиатеки (/music) и пути из navidrome.db для быстрой диагностики.
func (s *AdminServer) handleDebugFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	root := "/music"
	if s.guard != nil {
		root = s.guard.Root()
	}

	subpath := strings.TrimSpace(r.URL.Query().Get("sub"))
	if subpath != "" && !filepath.IsLocal(subpath) { // только внутри медиатеки: ни абсолютных путей, ни выхода через ..
		writeJSONError(w, http.StatusBadRequest, "sub must be a path inside the music folder")
		return
	}
	targetDir := root
	if subpath != "" {
		targetDir = filepath.Join(root, subpath)
	}

	fi, err := os.Stat(targetDir)
	exists := err == nil
	isDir := exists && fi.IsDir()

	var items []fsDebugItem
	if isDir {
		if entries, err := os.ReadDir(targetDir); err == nil {
			for _, e := range entries {
				info, _ := e.Info()
				size := int64(0)
				if info != nil && !e.IsDir() {
					size = info.Size()
				}
				items = append(items, fsDebugItem{
					Name:  e.Name(),
					Path:  filepath.Join(targetDir, e.Name()),
					IsDir: e.IsDir(),
					Size:  size,
				})
			}
		}
	}

	// Читаем пути из базы Navidrome для сравнения
	var libPaths []string
	var sampleMediaPaths []string
	if s.openNavDB != nil {
		if navDB, err := s.openNavDB(); err == nil && navDB != nil {
			defer navDB.Close()

			if rows, err := navDB.Query(`SELECT path FROM library`); err == nil {
				for rows.Next() {
					var p sql.NullString
					if err := rows.Scan(&p); err == nil && p.Valid {
						libPaths = append(libPaths, p.String)
					}
				}
				_ = rows.Close()
			}

			if rows, err := navDB.Query(`SELECT path FROM media_file LIMIT 10`); err == nil {
				for rows.Next() {
					var p sql.NullString
					if err := rows.Scan(&p); err == nil && p.Valid {
						sampleMediaPaths = append(sampleMediaPaths, p.String)
					}
				}
				_ = rows.Close()
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"root":             root,
		"targetDir":        targetDir,
		"exists":           exists,
		"isDir":            isDir,
		"itemsCount":       len(items),
		"items":            items,
		"navidromeLibrary": libPaths,
		"sampleMediaPaths": sampleMediaPaths,
		"normSample":       metadata.NormalizePathSpaces("Music\u00a0— копия"),
	})
}

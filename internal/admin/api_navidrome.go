// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_navidrome.go реализует эндпоинты для прямого подтягивания обложек альбомов
// и фотографий исполнителей из базы данных Navidrome (/api/v1/navidrome/artist-image
// и /api/v1/navidrome/album-cover) с фоллбэком на локальную файловую систему.
package admin

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/F5GO/poisq/internal/metadata"
)

// handleNavidromeArtistImage отдает изображение исполнителя из базы данных Navidrome
// (small_image_url, medium_image_url, large_image_url) либо локальный файл (artist.jpg).
func (s *AdminServer) handleNavidromeArtistImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	artistName := strings.TrimSpace(r.URL.Query().Get("artist"))
	if artistName == "" {
		writeJSONError(w, http.StatusBadRequest, "artist name is required")
		return
	}

	// 1. Проверяем наличие внешнего или кэшированного URL в navidrome.db
	if s.openNavDB != nil {
		if navDB, err := s.openNavDB(); err == nil && navDB != nil {
			defer navDB.Close()

			var smallURL, medURL, largeURL, imgFiles sql.NullString
			query := `
				SELECT small_image_url, medium_image_url, large_image_url, image_files
				FROM artist
				WHERE LOWER(name) = LOWER(?)
				LIMIT 1
			`
			if err := navDB.QueryRow(query, artistName).Scan(&smallURL, &medURL, &largeURL, &imgFiles); err == nil {
				// Приоритет отдаем самому высокому разрешению
				chosenURL := ""
				if largeURL.Valid && strings.TrimSpace(largeURL.String) != "" {
					chosenURL = strings.TrimSpace(largeURL.String)
				} else if medURL.Valid && strings.TrimSpace(medURL.String) != "" {
					chosenURL = strings.TrimSpace(medURL.String)
				} else if smallURL.Valid && strings.TrimSpace(smallURL.String) != "" {
					chosenURL = strings.TrimSpace(smallURL.String)
				}

				if chosenURL != "" {
					if strings.HasPrefix(chosenURL, "http://") || strings.HasPrefix(chosenURL, "https://") {
						w.Header().Set("Cache-Control", "public, max-age=86400")
						http.Redirect(w, r, chosenURL, http.StatusFound)
						return
					}
					// Если это локальный путь к файлу
					if fi, err := s.guard.ValidateTarget(chosenURL, false); err == nil {
						w.Header().Set("Cache-Control", "public, max-age=86400")
						http.ServeFile(w, r, fi)
						return
					}
				}

				// Если указан локальный путь в image_files
				if imgFiles.Valid && strings.TrimSpace(imgFiles.String) != "" {
					localImg := strings.TrimSpace(imgFiles.String)
					if fi, err := s.guard.ValidateTarget(localImg, false); err == nil {
						w.Header().Set("Cache-Control", "public, max-age=86400")
						http.ServeFile(w, r, fi)
						return
					}
				}
			}
		}
	}

	// 2. Фоллбэк: ищем фото артиста в каталоге исполнителя на диске
	if s.tracksRepo != nil && s.guard != nil {
		if tracks, err := s.tracksRepo.ListTracks("all", artistName, 5); err == nil && len(tracks) > 0 {
			root := s.guard.Root()
			for _, tr := range tracks {
				if tr.FolderPath != "" {
					artistFolder := filepath.Dir(tr.FolderPath)
					if found := metadata.FindCoverAnywhere(root, artistFolder); found != "" {
						if safe, err := s.guard.ValidateTarget(found, false); err == nil {
							w.Header().Set("Cache-Control", "public, max-age=86400")
							http.ServeFile(w, r, safe)
							return
						}
					}
				}
			}
		}
	}

	writeJSONError(w, http.StatusNotFound, "artist image not found")
}

// handleNavidromeAlbumCover отдает обложку альбома, проверяя базу Navidrome (cover_art_path)
// и универсальный локатор обложек на диске.
func (s *AdminServer) handleNavidromeAlbumCover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	albumName := strings.TrimSpace(r.URL.Query().Get("album"))
	artistName := strings.TrimSpace(r.URL.Query().Get("artist"))
	folderPath := strings.TrimSpace(r.URL.Query().Get("path"))

	root := ""
	if s.guard != nil {
		root = s.guard.Root()
	}

	// 1. Проверяем cover_art_path в navidrome.db
	if s.openNavDB != nil && albumName != "" {
		if navDB, err := s.openNavDB(); err == nil && navDB != nil {
			defer navDB.Close()

			var coverPath sql.NullString
			query := `
				SELECT cover_art_path
				FROM album
				WHERE LOWER(name) = LOWER(?)
				LIMIT 1
			`
			args := []any{albumName}
			if artistName != "" {
				query = `
					SELECT cover_art_path
					FROM album
					WHERE LOWER(name) = LOWER(?) AND LOWER(artist) = LOWER(?)
					LIMIT 1
				`
				args = append(args, artistName)
			}

			if err := navDB.QueryRow(query, args...).Scan(&coverPath); err == nil && coverPath.Valid {
				cp := strings.TrimSpace(coverPath.String)
				if cp != "" {
					if found := metadata.FindCoverAnywhere(root, cp); found != "" {
						w.Header().Set("Cache-Control", "public, max-age=86400")
						http.ServeFile(w, r, found)
						return
					}
				}
			}
		}
	}

	// 2. Фоллбэк: ищем обложку по пути папки
	if folderPath != "" {
		if found := metadata.FindCoverAnywhere(root, folderPath); found != "" {
			w.Header().Set("Cache-Control", "public, max-age=86400")
			http.ServeFile(w, r, found)
			return
		}
	}

	writeJSONError(w, http.StatusNotFound, "album cover not found")
}

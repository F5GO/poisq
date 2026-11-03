// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_media.go реализует безопасную отдачу аудиопотоков (/api/v1/tracks/stream)
// и локальных обложек альбомов (/api/v1/covers/local) для караоке-плеера и предпросмотра в Staging.
// Включает фоллбэк на извлечение встроенных обложек (ID3v2 APIC / FLAC PICTURE / ffmpeg),
// базу данных и кэш Navidrome, а также прямое проксирование потока из живого Navidrome.
package admin

import (
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/logger"
	"github.com/F5GO/poisq/internal/metadata"
	"github.com/F5GO/poisq/internal/proxy"
)

// handleTrackStream безопасно отдает аудиофайл браузеру с поддержкой Range-запросов
// для воспроизведения в караоке-плеере. Если локальный файл смонтирован по иному пути,
// автоматически проксирует поток из Navidrome (/rest/stream).
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

	root := ""
	if s.guard != nil {
		root = s.guard.Root()
	}

	searchTarget := track.FilePath
	if s.guard != nil {
		if safePath, err := s.guard.ValidateTarget(track.FilePath, false); err == nil {
			searchTarget = safePath
		} else {
			searchTarget = filepath.Join(root, strings.TrimPrefix(track.FilePath, "/"))
		}
	}

	found := metadata.FindAudioFileAnywhere(root, searchTarget)
	if found == "" && searchTarget != track.FilePath {
		found = metadata.FindAudioFileAnywhere(root, track.FilePath)
	}

	// Проверяем путь из navidrome.db, если в poisq.db путь относительный
	if found == "" && s.openNavDB != nil {
		if navDB, err := s.openNavDB(); err == nil && navDB != nil {
			libRoot := queryLibraryRoot(navDB, root)
			mfRow := findMediaFileRow(navDB, trackID, "", "")
			_ = navDB.Close()
			if p := strings.TrimSpace(mfRow["path"]); p != "" {
				found = metadata.FindAudioFileAnywhere(libRoot, p)
				if found == "" {
					found = metadata.FindAudioFileAnywhere(root, filepath.Join(libRoot, p))
				}
			}
		}
	}

	if found != "" {
		if s.guard != nil {
			if validated, err := s.guard.ValidateTarget(found, false); err == nil {
				found = validated
			}
		}
		http.ServeFile(w, r, found)
		return
	}

	// Фоллбэк: проксируем аудиопоток напрямую из сервера Navidrome (/rest/stream?id=...)
	if s.navidromeURL != "" {
		streamURL := s.navidromeURL + "/rest/stream?id=" + url.QueryEscape(trackID)
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, streamURL, nil)
		if err == nil {
			proxy.ApplyCachedAuthToRequest(req)
			if rng := r.Header.Get("Range"); rng != "" {
				req.Header.Set("Range", rng)
			}
			client := &http.Client{Timeout: 0} // Без таймаута для потокового аудио
			if resp, err := client.Do(req); err == nil {
				defer resp.Body.Close()
				ct := strings.ToLower(resp.Header.Get("Content-Type"))
				if (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) &&
					!strings.Contains(ct, "xml") && !strings.Contains(ct, "json") && !strings.Contains(ct, "html") {
					for k, vals := range resp.Header {
						for _, v := range vals {
							w.Header().Add(k, v)
						}
					}
					w.WriteHeader(resp.StatusCode)
					_, _ = io.Copy(w, resp.Body)
					return
				}
			}
		}
	}

	writeJSONError(w, http.StatusNotFound, "audio file not found")
}

// handleLocalCover безопасно отдает локальный файл обложки для предпросмотра и сравнения в Staging.
// Проверяет файлы обложек на диске (cover.jpg, folder.jpg и др.), встроенные в аудиофайлы теги
// (ID3v2 APIC / FLAC PICTURE / ffmpeg), базу и кэш Navidrome.
func (s *AdminServer) handleLocalCover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	coverPath := strings.TrimSpace(r.URL.Query().Get("path"))
	albumName := strings.TrimSpace(r.URL.Query().Get("album"))
	artistName := strings.TrimSpace(r.URL.Query().Get("artist"))

	logger.Debug("[COVER-API] [LOCAL] Запрос локальной обложки: path=%q, album=%q, artist=%q", coverPath, albumName, artistName)

	if coverPath == "" && albumName == "" && artistName == "" {
		writeJSONError(w, http.StatusBadRequest, "cover path or album is required")
		return
	}

	root := ""
	if s.guard != nil {
		root = s.guard.Root()
	}

	// 1. Поиск файла обложки или встроенной картинки по переданному пути
	if coverPath != "" {
		searchTarget := coverPath
		if s.guard != nil {
			if safePath, err := s.guard.ValidateTarget(coverPath, false); err == nil {
				searchTarget = safePath
			} else {
				searchTarget = filepath.Join(root, strings.TrimPrefix(coverPath, "/"))
			}
		}

		if s.serveCoverPathOrEmbedded(w, r, root, searchTarget, "") {
			logger.Debug("[COVER-API] [LOCAL] Обложка отдана по searchTarget=%q", searchTarget)
			return
		}
		if searchTarget != coverPath && s.serveCoverPathOrEmbedded(w, r, root, coverPath, "") {
			logger.Debug("[COVER-API] [LOCAL] Обложка отдана по coverPath=%q", coverPath)
			return
		}

		// Если artist/album не были переданы в query, извлекаем их из структуры пути папки
		if artistName == "" || albumName == "" {
			clean := filepath.Clean(coverPath)
			base := filepath.Base(clean)
			parent := filepath.Base(filepath.Dir(clean))
			if albumName == "" && base != "." && base != "/" {
				albumName = base
			}
			if artistName == "" && parent != "." && parent != "/" && !strings.EqualFold(parent, "music") {
				artistName = parent
			}
		}
	}

	// 2. Поиск по паре Исполнитель / Альбом на диске, в navidrome.db и в кэше Navidrome
	if albumName != "" || artistName != "" {
		logger.Debug("[COVER-API] [LOCAL] Делегирование в handleNavidromeAlbumCover: artist=%q, album=%q", artistName, albumName)
		q := r.URL.Query()
		if q.Get("album") == "" && albumName != "" {
			q.Set("album", albumName)
		}
		if q.Get("artist") == "" && artistName != "" {
			q.Set("artist", artistName)
		}
		r.URL.RawQuery = q.Encode()
		s.handleNavidromeAlbumCover(w, r)
		return
	}

	logger.Warn("[COVER-API] [LOCAL] Локальная обложка не найдена: path=%q, album=%q, artist=%q", coverPath, albumName, artistName)
	_ = time.Second
	writeJSONError(w, http.StatusNotFound, "cover file not found")
}

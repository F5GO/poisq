// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл api_navidrome.go реализует эндпоинты для безотказного подтягивания обложек альбомов
// и фотографий исполнителей из базы данных Navidrome (/api/v1/navidrome/artist-image
// и /api/v1/navidrome/album-cover), локальной файловой системы (/music), кэша Navidrome (/data/cache),
// встроенных тегов аудиофайлов (ID3v2 APIC / FLAC PICTURE / ffmpeg) и живого Subsonic API Navidrome.
package admin

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/logger"
	"github.com/F5GO/poisq/internal/metadata"
)

// SetNavidromeURL задает базовый URL сервера Navidrome (например, http://navidrome:4533)
// для прямого проксирования обложек и аудиопотоков.
func (s *AdminServer) SetNavidromeURL(u string) {
	s.navidromeURL = strings.TrimRight(strings.TrimSpace(u), "/")
}

// handleNavidromeArtistImage отдает изображение исполнителя, проверяя все доступные источники:
// 1. Таблицу artist в navidrome.db (с динамическим чтением колонок без риска "no such column").
// 2. Живой сервер Navidrome (/rest/getCoverArt?id=ar-<id>).
// 3. Папку артиста и дочерние папки его альбомов на диске (artist.jpg, cover.jpg, folder.jpg).
// 4. Встроенные в аудиофайлы этого артиста обложки (ID3v2 APIC / FLAC PICTURE).
// 5. Фоллбэк: iTunes / Cover Art для отображения актуального аватара артиста/альбома.
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

	logger.Debug("[ARTIST-IMAGE] Запрос фото исполнителя: %q", artistName)

	root := ""
	if s.guard != nil {
		root = s.guard.Root()
	}

	var artistID string

	// 1. Безопасное динамическое чтение таблицы artist из navidrome.db
	if s.openNavDB != nil {
		if navDB, err := s.openNavDB(); err == nil && navDB != nil {
			defer navDB.Close()

			libRoot := queryLibraryRoot(navDB, root)
			rowMap := findRowCaseInsensitive(navDB, "artist", "name", artistName)
			if len(rowMap) > 0 {
				artistID = rowMap["id"]

				// Проверяем локальные файлы изображений (uploaded_image, image_files)
				for _, col := range []string{"uploaded_image", "image_files", "external_image_url"} {
					if val := strings.TrimSpace(rowMap[col]); val != "" && !strings.HasPrefix(val, "http") {
						if s.serveCoverPathOrCache(w, r, libRoot, val) {
							return
						}
					}
				}

				// Проверяем внешние URL (large_image_url, medium_image_url, small_image_url),
				// отфильтровывая пустую заглушку-звезду Last.fm (2a96cbd8b46e442fc41c2b86b821562f)
				for _, col := range []string{"large_image_url", "medium_image_url", "small_image_url"} {
					u := strings.TrimSpace(rowMap[col])
					if u != "" && !strings.Contains(u, "2a96cbd8b46e442fc41c2b86b821562f") {
						if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
							w.Header().Set("Cache-Control", "public, max-age=86400")
							http.Redirect(w, r, u, http.StatusFound)
							return
						}
						if s.serveCoverPathOrCache(w, r, libRoot, u) {
							return
						}
					}
				}
			}

			// 2. Пробуем запросить картинку артиста у живого Navidrome или из его кэша /data/cache
			if artistID != "" {
				if s.tryServeFromNavidromeAPIOrCache(w, r, "ar-"+artistID, artistID) {
					return
				}
			}

			// 3. Проверяем альбомы этого артиста в navidrome.db
			albRow := findRowCaseInsensitive(navDB, "album", "album_artist", artistName)
			if len(albRow) == 0 {
				albRow = findRowCaseInsensitive(navDB, "album", "artist", artistName)
			}
			if len(albRow) > 0 {
				if s.tryServeAlbumRowCover(w, r, libRoot, albRow) {
					return
				}
			}
		}
	}

	// 4. Проверяем локальные папки артиста и его треков из poisq.db (включая дочерние папки альбомов и встроенные APIC/PICTURE)
	if s.tracksRepo != nil {
		if tracks, err := s.tracksRepo.ListTracks("all", artistName, 10); err == nil && len(tracks) > 0 {
			for _, tr := range tracks {
				// Сначала проверяем папку артиста (на уровень выше папки альбома)
				if tr.FolderPath != "" {
					artistFolder := filepath.Dir(tr.FolderPath)
					if s.serveCoverPathOrEmbedded(w, r, root, artistFolder, "") {
						return
					}
					// Затем саму папку альбома и аудиофайл трека
					if s.serveCoverPathOrEmbedded(w, r, root, tr.FolderPath, tr.FilePath) {
						return
					}
				}
				if tr.ID != "" && s.tryServeFromNavidromeAPIOrCache(w, r, "mf-"+tr.ID, tr.ID) {
					return
				}
			}
		}
	}

	// 5. Прямой поиск по имени артиста на диске
	if found := metadata.FindCoverByArtistAndAlbum(root, artistName, ""); found != "" {
		if s.guard != nil {
			validated, verr := s.guard.ValidateTarget(found, false)
			if verr != nil {
				writeJSONError(w, http.StatusForbidden, "forbidden cover path")
				return
			}
			found = validated
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, found)
		return
	}

	logger.Warn("[ARTIST-IMAGE] Фото исполнителя не найдено ни в одном источнике: %q", artistName)
	writeJSONError(w, http.StatusNotFound, "artist image not found")
}

// handleNavidromeAlbumCover отдает обложку альбома, проверяя:
// 1. Локальную файловую систему (по переданному path и по паре artist/album).
// 2. Базу данных Navidrome (таблицы album и media_file с реальными колонками embed_art_path, image_files, paths).
// 3. Встроенные обложки внутри аудиофайлов (ID3v2 APIC / FLAC PICTURE / ffmpeg).
// 4. Кэш Navidrome (/data/cache) и живой Subsonic API (/rest/getCoverArt).
func (s *AdminServer) handleNavidromeAlbumCover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	albumName := strings.TrimSpace(r.URL.Query().Get("album"))
	artistName := strings.TrimSpace(r.URL.Query().Get("artist"))
	folderPath := strings.TrimSpace(r.URL.Query().Get("path"))
	trackID := strings.TrimSpace(r.URL.Query().Get("trackId"))

	logger.Debug("[ALBUM-COVER] Запрос обложки: album=%q, artist=%q, path=%q, trackId=%q", albumName, artistName, folderPath, trackID)

	root := ""
	if s.guard != nil {
		root = s.guard.Root()
	}

	// 1. Сначала пробуем найти на диске или извлечь из тегов по переданному пути папки
	if folderPath != "" {
		if s.guard != nil {
			validated, verr := s.guard.ValidateTarget(folderPath, false)
			if verr != nil {
				writeJSONError(w, http.StatusForbidden, "forbidden cover path")
				return
			}
			folderPath = validated
		}
		if s.serveCoverPathOrEmbedded(w, r, root, folderPath, "") {
			return
		}
	}

	// 2. Проверяем в navidrome.db (таблицы album и media_file без привязки к жестким колонкам)
	if s.openNavDB != nil {
		if navDB, err := s.openNavDB(); err == nil && navDB != nil {
			defer navDB.Close()

			libRoot := queryLibraryRoot(navDB, root)
			if folderPath != "" && libRoot != root {
				if s.serveCoverPathOrEmbedded(w, r, libRoot, folderPath, "") {
					return
				}
			}

			if albumName != "" {
				albRow := findAlbumRowCaseInsensitive(navDB, albumName, artistName)
				if len(albRow) > 0 {
					if s.tryServeAlbumRowCover(w, r, libRoot, albRow) {
						return
					}
				}
			}

			// Ищем любой трек этого альбома в media_file
			mfRow := findMediaFileRow(navDB, trackID, albumName, artistName)
			if len(mfRow) > 0 {
				for _, col := range []string{"path", "embed_art_path"} {
					if p := strings.TrimSpace(mfRow[col]); p != "" {
						if s.serveCoverPathOrEmbedded(w, r, libRoot, filepath.Dir(p), p) {
							return
						}
					}
				}
				if mfID := mfRow["id"]; mfID != "" {
					if s.tryServeFromNavidromeAPIOrCache(w, r, "mf-"+mfID, mfID) {
						return
					}
				}
			}
		}
	}

	// 3. Проверяем через локальный каталог треков poisq.db
	if s.tracksRepo != nil {
		if trackID != "" {
			if tr, err := s.tracksRepo.GetByID(trackID); err == nil && tr != nil {
				if s.serveCoverPathOrEmbedded(w, r, root, tr.FolderPath, tr.FilePath) {
					return
				}
			}
		}
		q := albumName
		if q == "" {
			q = artistName
		}
		if q != "" {
			if tracks, err := s.tracksRepo.ListTracks("all", q, 5); err == nil {
				for _, tr := range tracks {
					if s.serveCoverPathOrEmbedded(w, r, root, tr.FolderPath, tr.FilePath) {
						return
					}
				}
			}
		}
	}

	// 4. Поиск на диске по названию артиста и альбома
	if artistName != "" || albumName != "" {
		if found := metadata.FindCoverByArtistAndAlbum(root, artistName, albumName); found != "" {
			if s.guard != nil {
				validated, verr := s.guard.ValidateTarget(found, false)
				if verr != nil {
					writeJSONError(w, http.StatusForbidden, "forbidden cover path")
					return
				}
				found = validated
			}
			w.Header().Set("Cache-Control", "public, max-age=86400")
			http.ServeFile(w, r, found)
			return
		}
	}

	logger.Warn("[ALBUM-COVER] Обложка альбома не найдена ни в одном источнике: album=%q, artist=%q, path=%q", albumName, artistName, folderPath)
	writeJSONError(w, http.StatusNotFound, "album cover not found")
}

// tryServeAlbumRowCover пытается отдать обложку по строке таблицы album из navidrome.db.
func (s *AdminServer) tryServeAlbumRowCover(w http.ResponseWriter, r *http.Request, libRoot string, albRow map[string]string) bool {
	// 1. Проверяем колонки с путями к картинкам или аудиофайлам с зашитой обложкой
	for _, col := range []string{"image_files", "embed_art_path", "cover_art_path", "paths"} {
		val := strings.TrimSpace(albRow[col])
		if val == "" {
			continue
		}
		// В Navidrome поля image_files и paths могут содержать несколько путей через разделитель (심вол \u0001, ;, или :)
		parts := strings.FieldsFunc(val, func(r rune) bool {
			return r == '\x01' || r == ';' || r == '|' || r == '\n'
		})
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if s.serveCoverPathOrEmbedded(w, r, libRoot, p, p) {
				return true
			}
		}
	}

	// 2. Пробуем через ID альбома в кэше Navidrome или через Subsonic API
	if albID := strings.TrimSpace(albRow["id"]); albID != "" {
		if s.tryServeFromNavidromeAPIOrCache(w, r, "al-"+albID, albID) {
			return true
		}
	}
	return false
}

// serveCoverPathOrEmbedded ищет графический файл обложки по пути, а если отдельного файла нет —
// извлекает встроенную обложку (ID3v2 APIC / FLAC PICTURE / ffmpeg) из аудиофайла.
func (s *AdminServer) serveCoverPathOrEmbedded(w http.ResponseWriter, r *http.Request, root, folderOrFile, explicitAudioFile string) bool {
	if s.serveCoverPathOrCache(w, r, root, folderOrFile) {
		return true
	}

	// Если отдельного графического файла cover.jpg нет, пробуем извлечь встроенную обложку из аудиофайла
	if explicitAudioFile != "" {
		resolvedAudio := metadata.FindAudioFileAnywhere(root, explicitAudioFile)
		if resolvedAudio == "" && !filepath.IsAbs(explicitAudioFile) && root != "" {
			resolvedAudio = metadata.FindAudioFileAnywhere(root, filepath.Join(root, explicitAudioFile))
		}
		if resolvedAudio != "" {
			if s.guard != nil {
				validated, err := s.guard.ValidateTarget(resolvedAudio, false)
				if err != nil {
					return false
				}
				resolvedAudio = validated
			}
			if data, mime := metadata.ExtractCoverFromAudioFile(resolvedAudio); len(data) > 0 {
				if ct, ok := safeImageContentType(mime, data); ok {
					logger.Debug("[COVER] Извлечена встроенная обложка из аудиофайла: %s", resolvedAudio)
					w.Header().Set("Content-Type", ct)
					w.Header().Set("Cache-Control", "public, max-age=86400")
					_, _ = w.Write(data)
					return true
				}
			}
		}
	}

	if folderOrFile != "" {
		candidates := metadata.GeneratePathVariants(root, folderOrFile)
		for _, cand := range candidates {
			resolvedDir := metadata.ResolveFuzzyDirectory(root, cand)
			if resolvedDir == "" {
				resolvedDir = cand
			}
			if s.guard != nil {
				validated, err := s.guard.ValidateTarget(resolvedDir, false)
				if err != nil {
					continue
				}
				resolvedDir = validated
			}
			if data, mime := metadata.ExtractCoverFromAudioOrDir(resolvedDir); len(data) > 0 {
				if ct, ok := safeImageContentType(mime, data); ok {
					logger.Debug("[COVER] Извлечена встроенная обложка из аудиофайлов каталога: %s", resolvedDir)
					w.Header().Set("Content-Type", ct)
					w.Header().Set("Cache-Control", "public, max-age=86400")
					_, _ = w.Write(data)
					return true
				}
			}
		}
	}

	return false
}

// serveCoverPathOrCache ищет картинку через FindCoverAnywhere и отдает ее.
func (s *AdminServer) serveCoverPathOrCache(w http.ResponseWriter, r *http.Request, root, targetPath string) bool {
	if strings.TrimSpace(targetPath) == "" {
		return false
	}
	found := metadata.FindCoverAnywhere(root, targetPath)
	if found != "" {
		if s.guard != nil {
			validated, err := s.guard.ValidateTarget(found, false)
			if err != nil {
				return false
			}
			found = validated
		}
		if fi, err := os.Stat(found); err == nil && !fi.IsDir() && fi.Size() > 0 {
			w.Header().Set("Cache-Control", "public, max-age=86400")
			http.ServeFile(w, r, found)
			return true
		}
	}
	return false
}

func safeImageContentType(rawMIME string, data []byte) (string, bool) {
	clean := strings.ToLower(strings.TrimSpace(strings.Split(rawMIME, ";")[0]))
	switch clean {
	case "image/jpeg", "image/jpg":
		return "image/jpeg", true
	case "image/png", "image/webp", "image/gif":
		return clean, true
	}
	detected := strings.ToLower(http.DetectContentType(data))
	switch detected {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return detected, true
	}
	return "", false
}

// tryServeFromNavidromeAPIOrCache проверяет кэш картинок Navidrome в /data/cache
// и делает прямой запрос к живому Navidrome (/rest/getCoverArt).
func (s *AdminServer) tryServeFromNavidromeAPIOrCache(w http.ResponseWriter, r *http.Request, coverArtID, rawID string) bool {
	// 1. Проверяем кэш-директорию Navidrome (/data/cache), которая лежит в общем томе /data
	for _, cacheRoot := range []string{"/data/cache", "/data/artwork"} {
		if found := findInNavidromeCache(cacheRoot, rawID, coverArtID); found != "" {
			w.Header().Set("Cache-Control", "public, max-age=86400")
			http.ServeFile(w, r, found)
			return true
		}
	}

	// 2. Запрашиваем у живого сервера Navidrome по HTTP (от служебной учётной записи Studio)

	client := &http.Client{Timeout: 5 * time.Second}
	for _, idCandidate := range []string{coverArtID, rawID} {
		if idCandidate == "" {
			continue
		}
		req, err := s.navidromeRequest(r.Context(), "getCoverArt", url.Values{"id": {idCandidate}})
		if errors.Is(err, errNoNavidromeAccount) {
			return false
		}
		if err != nil {
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if resp.StatusCode == http.StatusOK && strings.HasPrefix(ct, "image/") {
			w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = io.Copy(w, resp.Body)
			_ = resp.Body.Close()
			return true
		}
		_ = resp.Body.Close()
	}

	return false
}

func findInNavidromeCache(cacheDir string, ids ...string) string {
	fi, err := os.Stat(cacheDir)
	if err != nil || !fi.IsDir() {
		return ""
	}
	var matched string
	_ = filepath.WalkDir(cacheDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		nameLower := strings.ToLower(d.Name())
		for _, id := range ids {
			if id == "" {
				continue
			}
			if strings.Contains(nameLower, strings.ToLower(id)) {
				if info, err := d.Info(); err == nil && info.Size() > 100 {
					matched = path
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return matched
}

// queryLibraryRoot безопасно читает корневой путь библиотеки из таблицы library в navidrome.db.
func queryLibraryRoot(navDB *sql.DB, fallbackRoot string) string {
	if navDB == nil {
		return fallbackRoot
	}
	var p sql.NullString
	if err := navDB.QueryRow(`SELECT path FROM library LIMIT 1`).Scan(&p); err == nil && p.Valid {
		trimmed := strings.TrimSpace(p.String)
		if trimmed != "" {
			return trimmed
		}
	}
	return fallbackRoot
}

// findRowCaseInsensitive сначала выполняет быстрый целевой запрос с WHERE IN (варианты регистра)
// и переходит к полному обходу только при несовпадении смешанного регистра (например, «МакSим»).
func findRowCaseInsensitive(navDB *sql.DB, table, matchCol, targetVal string) map[string]string {
	targetVal = strings.TrimSpace(targetVal)
	if navDB == nil || targetVal == "" {
		return nil
	}
	if !allowedNavTableAndCol(table, matchCol) {
		return nil
	}

	if rows, err := navDB.Query(
		"SELECT * FROM "+table+" WHERE "+matchCol+" IN (?, ?, ?) LIMIT 1",
		targetVal, strings.ToLower(targetVal), strings.ToUpper(targetVal),
	); err == nil {
		cols, cerr := rows.Columns()
		if cerr == nil && rows.Next() {
			rowMap := scanDynamicRow(rows, cols)
			_ = rows.Close()
			if len(rowMap) > 0 {
				return rowMap
			}
		}
		_ = rows.Close()
	}

	rows, err := navDB.Query("SELECT * FROM " + table)
	if err != nil {
		return nil
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil
	}

	targetNorm := metadata.NormalizeNFC(targetVal)
	for rows.Next() {
		rowMap := scanDynamicRow(rows, cols)
		val := strings.TrimSpace(rowMap[strings.ToLower(matchCol)])
		if strings.EqualFold(metadata.NormalizeNFC(val), targetNorm) {
			return rowMap
		}
	}
	return nil
}

func allowedNavTableAndCol(table, col string) bool {
	switch table {
	case "artist":
		return col == "name" || col == "id"
	case "album":
		return col == "name" || col == "album_artist" || col == "artist" || col == "id"
	default:
		return false
	}
}

func findAlbumRowCaseInsensitive(navDB *sql.DB, albumName, artistName string) map[string]string {
	albumName = strings.TrimSpace(albumName)
	artistName = strings.TrimSpace(artistName)
	if navDB == nil || albumName == "" {
		return nil
	}

	albNorm := metadata.NormalizeNFC(albumName)
	artNorm := metadata.NormalizeNFC(artistName)
	var fallbackMatch map[string]string

	if rows, err := navDB.Query(
		"SELECT * FROM album WHERE name IN (?, ?, ?) LIMIT 25",
		albumName, strings.ToLower(albumName), strings.ToUpper(albumName),
	); err == nil {
		cols, cerr := rows.Columns()
		if cerr == nil {
			for rows.Next() {
				rowMap := scanDynamicRow(rows, cols)
				if artistName == "" {
					_ = rows.Close()
					return rowMap
				}
				albArt := metadata.NormalizeNFC(strings.TrimSpace(rowMap["album_artist"]))
				art := metadata.NormalizeNFC(strings.TrimSpace(rowMap["artist"]))
				if strings.EqualFold(albArt, artNorm) || strings.EqualFold(art, artNorm) {
					_ = rows.Close()
					return rowMap
				}
				if fallbackMatch == nil {
					fallbackMatch = rowMap
				}
			}
		}
		_ = rows.Close()
		if fallbackMatch != nil {
			return fallbackMatch
		}
	}

	rows, err := navDB.Query("SELECT * FROM album")
	if err != nil {
		return nil
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil
	}

	for rows.Next() {
		rowMap := scanDynamicRow(rows, cols)
		name := metadata.NormalizeNFC(strings.TrimSpace(rowMap["name"]))
		if !strings.EqualFold(name, albNorm) {
			continue
		}
		if artistName == "" {
			return rowMap
		}
		albArt := metadata.NormalizeNFC(strings.TrimSpace(rowMap["album_artist"]))
		art := metadata.NormalizeNFC(strings.TrimSpace(rowMap["artist"]))
		if strings.EqualFold(albArt, artNorm) || strings.EqualFold(art, artNorm) {
			return rowMap
		}
		if fallbackMatch == nil {
			fallbackMatch = rowMap
		}
	}
	return fallbackMatch
}

func findMediaFileRow(navDB *sql.DB, trackID, albumName, artistName string) map[string]string {
	if navDB == nil {
		return nil
	}

	if trackID != "" {
		rows, err := navDB.Query("SELECT * FROM media_file WHERE id = ? LIMIT 1", trackID)
		if err == nil {
			cols, _ := rows.Columns()
			if rows.Next() {
				m := scanDynamicRow(rows, cols)
				_ = rows.Close()
				return m
			}
			_ = rows.Close()
		}
	}

	if albumName == "" && artistName == "" {
		return nil
	}

	albNorm := metadata.NormalizeNFC(albumName)
	artNorm := metadata.NormalizeNFC(artistName)
	var fallback map[string]string

	if albumName != "" {
		if rows, err := navDB.Query(
			"SELECT * FROM media_file WHERE album IN (?, ?, ?) LIMIT 50",
			albumName, strings.ToLower(albumName), strings.ToUpper(albumName),
		); err == nil {
			cols, cerr := rows.Columns()
			if cerr == nil {
				for rows.Next() {
					rowMap := scanDynamicRow(rows, cols)
					art := metadata.NormalizeNFC(strings.TrimSpace(rowMap["artist"]))
					if artistName == "" || strings.EqualFold(art, artNorm) {
						_ = rows.Close()
						return rowMap
					}
					if fallback == nil {
						fallback = rowMap
					}
				}
			}
			_ = rows.Close()
			if fallback != nil {
				return fallback
			}
		}
	}

	rows, err := navDB.Query("SELECT * FROM media_file")
	if err != nil {
		return nil
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil
	}

	for rows.Next() {
		rowMap := scanDynamicRow(rows, cols)
		alb := metadata.NormalizeNFC(strings.TrimSpace(rowMap["album"]))
		art := metadata.NormalizeNFC(strings.TrimSpace(rowMap["artist"]))
		if albumName != "" && strings.EqualFold(alb, albNorm) {
			if artistName == "" || strings.EqualFold(art, artNorm) {
				return rowMap
			}
			if fallback == nil {
				fallback = rowMap
			}
		} else if albumName == "" && artistName != "" && strings.EqualFold(art, artNorm) {
			return rowMap
		}
	}
	return fallback
}

func scanDynamicRow(rows *sql.Rows, cols []string) map[string]string {
	rawVals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range rawVals {
		ptrs[i] = &rawVals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil
	}
	res := make(map[string]string, len(cols))
	for i, c := range cols {
		if rawVals[i].Valid {
			res[strings.ToLower(c)] = rawVals[i].String
		}
	}
	return res
}

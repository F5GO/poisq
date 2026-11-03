// Package db отвечает за безопасное взаимодействие с базами данных SQLite (navidrome.db и poisq.db).
//
// Данный файл repo_tracks.go управляет таблицей tracks в poisq.db: синхронизирует
// каталог файлов из Navidrome, хранит статусы текстов (none, plain, synced, verified),
// параметры обложек и предоставляет агрегированную статистику здоровья медиатеки.
package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"time"
)

// TrackRecord представляет состояние одного трека медиатеки в poisq.db.
type TrackRecord struct {
	ID            string `json:"id"`
	FilePath      string `json:"filePath"`
	FolderPath    string `json:"folderPath"`
	Artist        string `json:"artist"`
	Album         string `json:"album"`
	AlbumArtist   string `json:"albumArtist"`
	Title         string `json:"title"`
	TrackNumber   int    `json:"trackNumber"`
	Year          int    `json:"year"`
	Duration      int    `json:"duration"`
	LangCode      string `json:"langCode"`
	HasCover      bool   `json:"hasCover"`
	CoverWidth    int    `json:"coverWidth"`
	CoverHeight   int    `json:"coverHeight"`
	IsMojibake    bool   `json:"isMojibake"`
	LyricsStatus  string `json:"lyricsStatus"`
	ActiveSource  string `json:"activeSource"`
	ActiveLyrics  string `json:"activeLyrics"`
	LyricsBody    string `json:"lyricsBody"`
	LrcFileSynced bool   `json:"lrcFileSynced"`
	UpdatedAt     int64  `json:"updatedAt"`
}

// LibraryHealthStats содержит сводную статистику здоровья медиатеки для Дашборда.
type LibraryHealthStats struct {
	TotalTracks      int `json:"totalTracks"`
	TotalFolders     int `json:"totalFolders"`
	MissingCovers    int `json:"missingCovers"`
	LowResCovers     int `json:"lowResCovers"`
	MojibakeTracks   int `json:"mojibakeTracks"`
	LyricsNone       int `json:"lyricsNone"`
	LyricsPlain      int `json:"lyricsPlain"`
	LyricsSynced     int `json:"lyricsSynced"`
	LyricsVerified   int `json:"lyricsVerified"`
	PendingStaging   int `json:"pendingStaging"`
	PendingJobs      int `json:"pendingJobs"`
}

// TrackRepository управляет записями треков в poisq.db.
type TrackRepository struct {
	db *sql.DB
}

// NewTrackRepository создает экземпляр репозитория треков.
func NewTrackRepository(db *sql.DB) *TrackRepository {
	return &TrackRepository{db: db}
}

// UpsertTrack добавляет или обновляет базовые метаданные трека (не затирая уже найденные тексты).
func (r *TrackRepository) UpsertTrack(t TrackRecord) error {
	now := time.Now().Unix()
	if t.FolderPath == "" && t.FilePath != "" {
		t.FolderPath = filepath.Dir(t.FilePath)
	}
	if t.LyricsStatus == "" {
		t.LyricsStatus = "none"
	}

	hasCoverInt := 0
	if t.HasCover {
		hasCoverInt = 1
	}
	mojibakeInt := 0
	if t.IsMojibake {
		mojibakeInt = 1
	}
	lrcSyncedInt := 0
	if t.LrcFileSynced {
		lrcSyncedInt = 1
	}

	// Защита от дублей: если Navidrome перегенерировал ID трека при обновлении тегов,
	// удаляем старую запись с тем же file_path, но другим ID
	if t.FilePath != "" {
		_, _ = r.db.Exec(`DELETE FROM tracks WHERE file_path = ? AND id != ?`, t.FilePath, t.ID)
	}

	_, err := r.db.Exec(`
		INSERT INTO tracks (
			id, file_path, folder_path, artist, album, album_artist, title,
			track_number, year, duration, lang_code, has_cover, cover_width, cover_height,
			is_mojibake, lyrics_status, active_source, active_lyrics, lrc_file_synced, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			file_path = excluded.file_path,
			folder_path = excluded.folder_path,
			artist = excluded.artist,
			album = excluded.album,
			album_artist = excluded.album_artist,
			title = excluded.title,
			track_number = excluded.track_number,
			year = excluded.year,
			duration = excluded.duration,
			has_cover = CASE WHEN excluded.has_cover = 1 THEN 1 ELSE tracks.has_cover END,
			cover_width = CASE WHEN excluded.cover_width > 0 THEN excluded.cover_width ELSE tracks.cover_width END,
			cover_height = CASE WHEN excluded.cover_height > 0 THEN excluded.cover_height ELSE tracks.cover_height END,
			is_mojibake = excluded.is_mojibake,
			updated_at = excluded.updated_at
	`,
		t.ID, t.FilePath, t.FolderPath, t.Artist, t.Album, t.AlbumArtist, t.Title,
		t.TrackNumber, t.Year, t.Duration, t.LangCode, hasCoverInt, t.CoverWidth, t.CoverHeight,
		mojibakeInt, t.LyricsStatus, t.ActiveSource, t.ActiveLyrics, lrcSyncedInt, now,
	)
	return err
}

// UpsertTracksBatch пакетно сохраняет срез записей треков внутри единой транзакции SQLite,
// предотвращая зависание и снижая нагрузку на диск в десятки раз при первичной синхронизации.
func (r *TrackRepository) UpsertTracksBatch(tracks []TrackRecord) error {
	if len(tracks) == 0 {
		return nil
	}

	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO tracks (
			id, file_path, folder_path, artist, album, album_artist, title,
			track_number, year, duration, lang_code, has_cover, cover_width, cover_height,
			is_mojibake, lyrics_status, active_source, active_lyrics, lrc_file_synced, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			file_path = excluded.file_path,
			folder_path = excluded.folder_path,
			artist = excluded.artist,
			album = excluded.album,
			album_artist = excluded.album_artist,
			title = excluded.title,
			track_number = excluded.track_number,
			year = excluded.year,
			duration = excluded.duration,
			has_cover = CASE WHEN excluded.has_cover = 1 THEN 1 ELSE tracks.has_cover END,
			cover_width = CASE WHEN excluded.cover_width > 0 THEN excluded.cover_width ELSE tracks.cover_width END,
			cover_height = CASE WHEN excluded.cover_height > 0 THEN excluded.cover_height ELSE tracks.cover_height END,
			is_mojibake = excluded.is_mojibake,
			updated_at = excluded.updated_at
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for _, t := range tracks {
		hasCoverInt := 0
		if t.HasCover {
			hasCoverInt = 1
		}
		mojibakeInt := 0
		if t.IsMojibake {
			mojibakeInt = 1
		}
		lrcSyncedInt := 0
		if t.LrcFileSynced {
			lrcSyncedInt = 1
		}
		status := t.LyricsStatus
		if status == "" {
			status = "none"
		}

		_, err := stmt.Exec(
			t.ID, t.FilePath, t.FolderPath, t.Artist, t.Album, t.AlbumArtist, t.Title,
			t.TrackNumber, t.Year, t.Duration, t.LangCode, hasCoverInt, t.CoverWidth, t.CoverHeight,
			mojibakeInt, status, t.ActiveSource, t.ActiveLyrics, lrcSyncedInt, now,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// UpdateLyrics обновляет статус, источник и содержимое текста для трека.
func (r *TrackRepository) UpdateLyrics(trackID, status, source, content, langCode string, lrcWritten bool) error {
	now := time.Now().Unix()
	lrcInt := 0
	if lrcWritten {
		lrcInt = 1
	}
	_, err := r.db.Exec(`
		UPDATE tracks
		SET lyrics_status = ?, active_source = ?, active_lyrics = ?,
		    lang_code = CASE WHEN ? != '' THEN ? ELSE lang_code END,
		    lrc_file_synced = ?, updated_at = ?
		WHERE id = ?
	`, status, source, content, langCode, langCode, lrcInt, now, trackID)
	return err
}

// ClearLyrics полностью удаляет сохраненный или сгенерированный текст у трека и сбрасывает статус в 'none'.
func (r *TrackRepository) ClearLyrics(trackID string) error {
	now := time.Now().Unix()
	_, err := r.db.Exec(`
		UPDATE tracks
		SET lyrics_status = 'none', active_source = '', active_lyrics = '',
		    lrc_file_synced = 0, updated_at = ?
		WHERE id = ?
	`, now, trackID)
	return err
}

// UpdateMetadata обновляет теги трека в poisq.db после применения Staging-предложения или отката.
func (r *TrackRepository) UpdateMetadata(trackID, artist, album, albumArtist, title string, year int, isMojibake bool) error {
	now := time.Now().Unix()
	mojibakeInt := 0
	if isMojibake {
		mojibakeInt = 1
	}
	_, err := r.db.Exec(`
		UPDATE tracks
		SET artist = ?, album = ?, album_artist = ?, title = ?, year = ?, is_mojibake = ?, updated_at = ?
		WHERE id = ?
	`, artist, album, albumArtist, title, year, mojibakeInt, now, trackID)
	return err
}

// UpdateCoverInfo обновляет сведения об обложке трека или папки.
func (r *TrackRepository) UpdateCoverInfo(folderPath string, hasCover bool, width, height int) error {
	hasInt := 0
	if hasCover {
		hasInt = 1
	}
	_, err := r.db.Exec(`
		UPDATE tracks
		SET has_cover = ?, cover_width = ?, cover_height = ?, updated_at = ?
		WHERE folder_path = ?
	`, hasInt, width, height, time.Now().Unix(), folderPath)
	return err
}

// GetByID возвращает трек по его ID.
func (r *TrackRepository) GetByID(id string) (*TrackRecord, error) {
	row := r.db.QueryRow(`
		SELECT id, file_path, folder_path, artist, album, album_artist, title,
		       track_number, year, duration, lang_code, has_cover, cover_width, cover_height,
		       is_mojibake, lyrics_status, active_source, active_lyrics, lrc_file_synced, updated_at
		FROM tracks WHERE id = ?
	`, id)
	return scanTrackRow(row)
}

// ListTracks возвращает список треков с фильтрацией по статусу текста, кодировке и поисковому запросу.
func (r *TrackRepository) ListTracks(statusFilter string, searchQuery string, limit int) ([]TrackRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := `
		SELECT id, file_path, folder_path, artist, album, album_artist, title,
		       track_number, year, duration, lang_code, has_cover, cover_width, cover_height,
		       is_mojibake, lyrics_status, active_source, active_lyrics, lrc_file_synced, updated_at
		FROM tracks WHERE 1=1
	`
	var args []any

	if statusFilter != "" && statusFilter != "all" {
		if statusFilter == "mojibake" {
			query += ` AND is_mojibake = 1`
		} else if statusFilter == "no_cover" {
			query += ` AND (has_cover = 0 OR (cover_width > 0 AND cover_width < 500))`
		} else {
			query += ` AND lyrics_status = ?`
			args = append(args, statusFilter)
		}
	}

	if q := strings.TrimSpace(searchQuery); q != "" {
		like := "%" + q + "%"
		query += ` AND (artist LIKE ? OR title LIKE ? OR album LIKE ? OR file_path LIKE ?)`
		args = append(args, like, like, like, like)
	}

	query += ` ORDER BY updated_at DESC, artist ASC, album ASC, track_number ASC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []TrackRecord
	for rows.Next() {
		t, err := scanTrackRows(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *t)
	}
	return list, rows.Err()
}

// ListByFolder возвращает все треки в указанной папке для правила Folder-as-Album Lock.
func (r *TrackRepository) ListByFolder(folderPath string) ([]TrackRecord, error) {
	rows, err := r.db.Query(`
		SELECT id, file_path, folder_path, artist, album, album_artist, title,
		       track_number, year, duration, lang_code, has_cover, cover_width, cover_height,
		       is_mojibake, lyrics_status, active_source, active_lyrics, lrc_file_synced, updated_at
		FROM tracks WHERE folder_path = ? ORDER BY track_number ASC, title ASC
	`, folderPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []TrackRecord
	for rows.Next() {
		t, err := scanTrackRows(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *t)
	}
	return list, rows.Err()
}

// GetHealthStats вычисляет полную статистику здоровья медиатеки для Дашборда.
func (r *TrackRepository) GetHealthStats() (LibraryHealthStats, error) {
	var s LibraryHealthStats
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM tracks`).Scan(&s.TotalTracks)
	_ = r.db.QueryRow(`SELECT COUNT(DISTINCT folder_path) FROM tracks WHERE folder_path != ''`).Scan(&s.TotalFolders)
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM tracks WHERE has_cover = 0`).Scan(&s.MissingCovers)
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM tracks WHERE has_cover = 1 AND cover_width > 0 AND (cover_width < 500 OR cover_height < 500)`).Scan(&s.LowResCovers)
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM tracks WHERE is_mojibake = 1`).Scan(&s.MojibakeTracks)
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM tracks WHERE lyrics_status = 'none'`).Scan(&s.LyricsNone)
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM tracks WHERE lyrics_status = 'plain'`).Scan(&s.LyricsPlain)
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM tracks WHERE lyrics_status = 'synced'`).Scan(&s.LyricsSynced)
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM tracks WHERE lyrics_status = 'verified'`).Scan(&s.LyricsVerified)
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM staging_changes WHERE status = 'pending'`).Scan(&s.PendingStaging)
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM lyrics_jobs WHERE status IN ('pending', 'running', 'waiting_quota')`).Scan(&s.PendingJobs)
	return s, nil
}

func scanTrackRow(row *sql.Row) (*TrackRecord, error) {
	var t TrackRecord
	var hasCover, isMojibake, lrcSynced int
	err := row.Scan(
		&t.ID, &t.FilePath, &t.FolderPath, &t.Artist, &t.Album, &t.AlbumArtist, &t.Title,
		&t.TrackNumber, &t.Year, &t.Duration, &t.LangCode, &hasCover, &t.CoverWidth, &t.CoverHeight,
		&isMojibake, &t.LyricsStatus, &t.ActiveSource, &t.ActiveLyrics, &lrcSynced, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	t.HasCover = hasCover == 1
	t.IsMojibake = isMojibake == 1
	t.LrcFileSynced = lrcSynced == 1
	t.LyricsBody = t.ActiveLyrics
	return &t, nil
}

func scanTrackRows(rows *sql.Rows) (*TrackRecord, error) {
	var t TrackRecord
	var hasCover, isMojibake, lrcSynced int
	err := rows.Scan(
		&t.ID, &t.FilePath, &t.FolderPath, &t.Artist, &t.Album, &t.AlbumArtist, &t.Title,
		&t.TrackNumber, &t.Year, &t.Duration, &t.LangCode, &hasCover, &t.CoverWidth, &t.CoverHeight,
		&isMojibake, &t.LyricsStatus, &t.ActiveSource, &t.ActiveLyrics, &lrcSynced, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	t.HasCover = hasCover == 1
	t.IsMojibake = isMojibake == 1
	t.LrcFileSynced = lrcSynced == 1
	t.LyricsBody = t.ActiveLyrics
	return &t, nil
}

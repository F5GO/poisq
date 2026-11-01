// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл writer.go реализует опциональную и безопасную запись файлов текстов (.lrc)
// на диск рядом с медиафайлами трека в медиатеке Navidrome (только при явном включении SaveToDisk).
package lyrics

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// DiskWriter инкапсулирует логику сохранения файлов .lrc на файловую систему.
type DiskWriter struct {
	enabled   bool
	musicPath string
	db        *sql.DB
}

// NewDiskWriter создает экземпляр писателя на диск.
func NewDiskWriter(enabled bool, musicPath string, db *sql.DB) *DiskWriter {
	return &DiskWriter{
		enabled:   enabled,
		musicPath: musicPath,
		db:        db,
	}
}

// SaveAsync асинхронно сохраняет файл .lrc на диск, если опция активна и есть текст.
func (w *DiskWriter) SaveAsync(q SearchQuery, lyr *Lyrics) {
	if !w.enabled || lyr == nil {
		return
	}

	content := lyr.SyncedLyrics
	if content == "" {
		content = lyr.PlainLyrics
	}
	if strings.TrimSpace(content) == "" {
		return
	}

	go func() {
		if err := w.save(q, content); err != nil {
			log.Printf("[DEBUG] [LYRICS] Не удалось сохранить .lrc на диск для %q - %q: %v", q.Artist, q.Title, err)
		}
	}()
}

func (w *DiskWriter) save(q SearchQuery, content string) error {
	targetLrcPath := ""

	// 1. Если передан TrackID и доступна БД Navidrome, находим реальный путь к файлу трека
	if q.TrackID != "" && w.db != nil {
		var audioPath string
		err := w.db.QueryRow("SELECT path FROM media_file WHERE id = ? LIMIT 1", q.TrackID).Scan(&audioPath)
		if err == nil && audioPath != "" {
			ext := filepath.Ext(audioPath)
			targetLrcPath = strings.TrimSuffix(audioPath, ext) + ".lrc"
		}
	}

	// 2. Если по TrackID не нашли, пробуем найти по artist и title в media_file
	if targetLrcPath == "" && w.db != nil && q.Artist != "" && q.Title != "" {
		var audioPath string
		err := w.db.QueryRow(
			"SELECT path FROM media_file WHERE artist = ? AND title = ? LIMIT 1",
			q.Artist, q.Title,
		).Scan(&audioPath)
		if err == nil && audioPath != "" {
			ext := filepath.Ext(audioPath)
			targetLrcPath = strings.TrimSuffix(audioPath, ext) + ".lrc"
		}
	}

	// 3. Запасной путь в структурированной папке _lyrics
	if targetLrcPath == "" {
		safeArtist := sanitizeFilename(q.Artist)
		safeTitle := sanitizeFilename(q.Title)
		if safeArtist == "" || safeTitle == "" {
			return fmt.Errorf("empty artist or title for lrc filename")
		}
		targetLrcPath = filepath.Join(w.musicPath, safeArtist, safeTitle+".lrc")
	}

	// Защита от Path Traversal
	cleanTarget := filepath.Clean(targetLrcPath)
	dir := filepath.Dir(cleanTarget)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Не перезаписываем, если файл уже существует
	if _, err := os.Stat(cleanTarget); err == nil {
		return nil
	}

	return os.WriteFile(cleanTarget, []byte(content), 0644)
}

func sanitizeFilename(name string) string {
	var sb strings.Builder
	for _, r := range name {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			sb.WriteRune('_')
		} else {
			sb.WriteRune(r)
		}
	}
	return strings.TrimSpace(sb.String())
}

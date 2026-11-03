// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл cover_locator.go реализует надежный и регистронезависимый поиск существующих локальных
// файлов обложек в папке альбома (cover.jpg, folder.jpg, front.png, webp и любые вариации регистра).
package metadata

import (
	"os"
	"path/filepath"
	"strings"
)

var priorityCoverBases = []string{
	"cover",
	"folder",
	"front",
	"album",
	"art",
}

var supportedImageExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
}

// FindLocalCoverFile сканирует директорию folderPath и возвращает абсолютный путь
// к существующему файлу обложки альбома с наивысшим приоритетом (cover.jpg > folder.jpg > etc.).
// Если файл не найден, возвращает пустую строку.
func FindLocalCoverFile(folderPath string) string {
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return ""
	}

	// 1-й проход: ищем файлы с приоритетными базовыми именами
	for _, baseName := range priorityCoverBases {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			nameLower := strings.ToLower(entry.Name())
			ext := filepath.Ext(nameLower)
			if !supportedImageExts[ext] {
				continue
			}
			base := strings.TrimSuffix(nameLower, ext)
			if base == baseName {
				full := filepath.Join(folderPath, entry.Name())
				if fi, err := entry.Info(); err == nil && fi.Size() > 0 {
					return full
				}
			}
		}
	}

	// 2-й проход: ищем любые другие графические файлы в папке
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		nameLower := strings.ToLower(entry.Name())
		ext := filepath.Ext(nameLower)
		if supportedImageExts[ext] {
			full := filepath.Join(folderPath, entry.Name())
			if fi, err := entry.Info(); err == nil && fi.Size() > 0 {
				return full
			}
		}
	}

	return ""
}

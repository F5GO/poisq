// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл cover_locator.go реализует надежный и регистронезависимый поиск существующих локальных
// файлов обложек в папке альбома (cover.jpg, folder.jpg, front.png, webp и любые вариации регистра),
// с устойчивостью к различиям в тире/дефисах (- / – / —), нормализации Unicode (NFC/NFD) и проверкой родительских папок.
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
// Если папка не найдена напрямую, выполняет нечеткий поиск папки и проверяет родительскую директорию.
func FindLocalCoverFile(folderPath string) string {
	cleanPath := filepath.Clean(strings.TrimSpace(folderPath))
	if cleanPath == "" || cleanPath == "." || cleanPath == "/" {
		return ""
	}

	// 1. Прямой поиск в указанной папке
	if found := scanDirectoryForCover(cleanPath); found != "" {
		return found
	}

	// 2. Если папка не открылась, пробуем найти реальный каталог в родителе
	// с учетом различий в дефисах/тире (- vs – vs —) и регистре
	parent := filepath.Dir(cleanPath)
	targetBase := normalizeNameForMatch(filepath.Base(cleanPath))

	if parent != cleanPath && parent != "." && parent != "/" {
		if pEntries, err := os.ReadDir(parent); err == nil {
			for _, pe := range pEntries {
				if !pe.IsDir() {
					continue
				}
				if normalizeNameForMatch(pe.Name()) == targetBase {
					realFolder := filepath.Join(parent, pe.Name())
					if found := scanDirectoryForCover(realFolder); found != "" {
						return found
					}
				}
			}

			// 3. Если в самой папке трека обложки нет, проверяем родительскую папку
			// (актуально, если cover.jpg лежит на уровне альбома или исполнителя)
			if found := scanDirectoryForCover(parent); found != "" {
				return found
			}
		}
	}

	return ""
}

func scanDirectoryForCover(dirPath string) string {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return ""
	}

	// 1-й проход: ищем файлы с приоритетными базовыми именами (cover, folder, front, album, art)
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
				full := filepath.Join(dirPath, entry.Name())
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
			full := filepath.Join(dirPath, entry.Name())
			if fi, err := entry.Info(); err == nil && fi.Size() > 0 {
				return full
			}
		}
	}

	return ""
}

func normalizeNameForMatch(s string) string {
	res := strings.ToLower(strings.TrimSpace(s))
	res = strings.ReplaceAll(res, "–", "-")
	res = strings.ReplaceAll(res, "—", "-")
	res = strings.ReplaceAll(res, "_", "-")
	res = strings.ReplaceAll(res, "  ", " ")
	return res
}

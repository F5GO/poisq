// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл audio_locator.go реализует надежный и устойчивый поиск аудиофайлов треков
// на файловой системе медиатеки. Разрешает расхождения в дефисах/тире (- vs – vs —),
// суффиксах копий (« — копия»), нормализации Unicode (NFC/NFD на macOS APFS),
// вариациях префикса библиотеки (/music/Music vs /music) и регистре расширений файлов.
package metadata

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/F5GO/poisq/internal/logger"
)

// FindAudioFileAnywhere выполняет поиск реального аудиофайла на диске,
// разрешая любые неточности в путях между базой данных Navidrome и файловой системой хоста.
func FindAudioFileAnywhere(musicRoot, targetPath string) string {
	clean := filepath.Clean(strings.TrimSpace(targetPath))
	if clean == "" || clean == "." || clean == "/" {
		return ""
	}

	// 1. Прямая проверка: если файл существует по указанному пути
	if fi, err := os.Stat(clean); err == nil && !fi.IsDir() && fi.Size() > 0 {
		return clean
	}

	candidatePaths := GeneratePathVariants(musicRoot, targetPath)
	if len(candidatePaths) == 0 {
		candidatePaths = []string{clean}
	}

	for _, cand := range candidatePaths {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Size() > 0 {
			logger.Debug("[AUDIO] Аудиофайл найден напрямую: %s", cand)
			return cand
		}

		targetDir := filepath.Dir(cand)
		targetFileName := filepath.Base(cand)

		resolvedDir := ResolveFuzzyDirectory(musicRoot, targetDir)
		if resolvedDir == "" {
			resolvedDir = targetDir
		}

		if found := matchAudioFileInDirectory(resolvedDir, targetFileName); found != "" {
			logger.Debug("[AUDIO] Аудиофайл найден в каталоге: %s (запрос: %s)", found, targetPath)
			return found
		}
	}

	logger.Debug("[AUDIO] Аудиофайл не найден на диске для пути %q (проверено вариантов: %d)", targetPath, len(candidatePaths))
	return ""
}

// matchAudioFileInDirectory сопоставляет имя файла внутри каталога dirPath с учетом регистра,
// формы Unicode, нормализации тире и поддерживаемых расширений.
func matchAudioFileInDirectory(dirPath, targetFileName string) string {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return ""
	}

	normTarget := normalizeNameForMatch(targetFileName)
	targetExt := strings.ToLower(filepath.Ext(targetFileName))
	targetBase := strings.TrimSuffix(normTarget, normalizeNameForMatch(targetExt))

	// 1-й проход: точное совпадение с учетом нормализации тире/Unicode
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		normEntry := normalizeNameForMatch(entry.Name())
		if normEntry == normTarget {
			full := filepath.Join(dirPath, entry.Name())
			if fi, err := entry.Info(); err == nil && fi.Size() > 0 {
				return full
			}
		}
	}

	// 2-й проход: совпадение по имени без расширения
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		entryExt := strings.ToLower(filepath.Ext(entry.Name()))
		if !isSupportedAudioExt(entryExt) {
			continue
		}
		normEntry := normalizeNameForMatch(entry.Name())
		entryBase := strings.TrimSuffix(normEntry, normalizeNameForMatch(entryExt))

		if entryBase == targetBase {
			full := filepath.Join(dirPath, entry.Name())
			if fi, err := entry.Info(); err == nil && fi.Size() > 0 {
				return full
			}
		}
	}

	// 3-й проход: частичное вхождение
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		entryExt := strings.ToLower(filepath.Ext(entry.Name()))
		if !isSupportedAudioExt(entryExt) {
			continue
		}
		normEntry := normalizeNameForMatch(entry.Name())
		entryBase := strings.TrimSuffix(normEntry, normalizeNameForMatch(entryExt))

		if len(targetBase) > 4 && len(entryBase) > 4 {
			if strings.Contains(entryBase, targetBase) || strings.Contains(targetBase, entryBase) {
				full := filepath.Join(dirPath, entry.Name())
				if fi, err := entry.Info(); err == nil && fi.Size() > 0 {
					return full
				}
			}
		}
	}

	return ""
}

// isSupportedAudioExt проверяет, поддерживается ли аудиоформат для воспроизведения.
func isSupportedAudioExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp3", ".flac", ".m4a", ".aac", ".ogg", ".opus", ".wav", ".alac":
		return true
	default:
		return false
	}
}

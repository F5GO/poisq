// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл audio_locator.go реализует надежный и устойчивый поиск аудиофайлов треков
// на файловой системе медиатеки. Разрешает расхождения в дефисах/тире (- vs – vs —),
// нормализации Unicode (NFC/NFD на macOS APFS), вариациях префикса библиотеки (/music/Music vs /music)
// и регистре расширений файлов для бесперебойного воспроизведения в караоке-плеере.
package metadata

import (
	"os"
	"path/filepath"
	"strings"
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

	targetDir := filepath.Dir(clean)
	targetFileName := filepath.Base(clean)

	// 2. Разрешаем директорию с нечетким сопоставлением (тире, Unicode, префикс Music)
	resolvedDir := ResolveFuzzyDirectory(musicRoot, targetDir)
	if resolvedDir == "" {
		resolvedDir = targetDir
	}

	// 3. Ищем аудиофайл внутри найденного каталога
	if found := matchAudioFileInDirectory(resolvedDir, targetFileName); found != "" {
		return found
	}

	// 4. Если каталог не разрешился или файл не найден, пробуем поискать в родительских папках
	if musicRoot != "" && strings.HasPrefix(targetDir, musicRoot) {
		trimmedRel := strings.TrimPrefix(targetDir, musicRoot)
		trimmedRel = strings.TrimPrefix(trimmedRel, "/")
		// Пробуем отрезать префикс Music/ если он есть
		for _, prefix := range []string{"Music/", "music/", "Audio/", "audio/"} {
			if strings.HasPrefix(trimmedRel, prefix) {
				altDir := filepath.Join(musicRoot, strings.TrimPrefix(trimmedRel, prefix))
				if altResolved := ResolveFuzzyDirectory(musicRoot, altDir); altResolved != "" {
					if found := matchAudioFileInDirectory(altResolved, targetFileName); found != "" {
						return found
					}
				}
			}
		}
	}

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

	// 2-й проход: совпадение по имени без расширения (на случай если расширение в другом регистре или формате)
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

	// 3-й проход: частичное вхождение (если в названии файла на диске опущен или добавлен номер дорожки)
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

		// Проверяем, содержится ли значимая часть названия
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

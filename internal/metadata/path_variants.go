// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл path_variants.go генерирует все допустимые вариации путей к папкам медиатеки,
// разрешая расхождения между базой данных Navidrome и файловой системой:
// - Суффиксы дубликатов папок (« — копия», « - копия», « (копия)», «_copy», « (1)»);
// - Префиксы корневых папок (/music vs /music/Music vs /music/Music — копия);
// - Различия в тире и дефисах (- vs – vs —) и форме Unicode NFC/NFD.
package metadata

import (
	"path/filepath"
	"strings"
)

var copySuffixes = []string{
	" — копия",
	" – копия",
	" - копия",
	" (копия)",
	" копия",
	"_copy",
	"-copy",
	" (1)",
	" (2)",
}

// GeneratePathVariants создает упорядоченный список потенциальных путей к папке на диске,
// перебирая все типовые расхождения структуры каталогов между Navidrome и хостом.
func GeneratePathVariants(musicRoot, targetPath string) []string {
	clean := filepath.Clean(strings.TrimSpace(targetPath))
	if clean == "" || clean == "." || clean == "/" {
		return nil
	}

	variants := make([]string, 0, 16)
	seen := make(map[string]bool)

	addVariant := func(p string) {
		p = filepath.Clean(strings.TrimSpace(p))
		if p != "" && p != "." && p != "/" && !seen[p] {
			seen[p] = true
			variants = append(variants, p)
		}
	}

	// 1. Исходный путь
	addVariant(clean)

	// 2. Если путь относительный — добавляем относительно musicRoot
	if !filepath.IsAbs(clean) && musicRoot != "" {
		addVariant(filepath.Join(musicRoot, clean))
	}

	// 3. Варианты с устранением суффиксов «копия» в каждом сегменте пути
	cleanNoCopy := clean
	hasCopySegment := false
	for _, suf := range copySuffixes {
		if strings.Contains(cleanNoCopy, suf) {
			cleanNoCopy = strings.ReplaceAll(cleanNoCopy, suf, "")
			hasCopySegment = true
		}
	}
	if hasCopySegment {
		addVariant(cleanNoCopy)
		if !filepath.IsAbs(cleanNoCopy) && musicRoot != "" {
			addVariant(filepath.Join(musicRoot, cleanNoCopy))
		}
	}

	// 4. Варианты с манипуляцией префиксом /music и /music/Music
	currentPaths := make([]string, len(variants))
	copy(currentPaths, variants)

	for _, p := range currentPaths {
		// Отрезаем /music корень
		trimmed := p
		if strings.HasPrefix(trimmed, "/music/") {
			trimmed = strings.TrimPrefix(trimmed, "/music/")
		} else if strings.HasPrefix(trimmed, "/music") {
			trimmed = strings.TrimPrefix(trimmed, "/music")
		}
		trimmed = strings.TrimPrefix(trimmed, "/")

		// Вариант без промежуточного Music/ или Music — копия/
		for _, prefixToRemove := range []string{"Music/", "music/", "Music — копия/", "Music - копия/", "Audio/", "audio/"} {
			if strings.HasPrefix(trimmed, prefixToRemove) {
				withoutPrefix := strings.TrimPrefix(trimmed, prefixToRemove)
				addVariant(withoutPrefix)
				if musicRoot != "" {
					addVariant(filepath.Join(musicRoot, withoutPrefix))
					addVariant(filepath.Join(musicRoot, "Music", withoutPrefix))
				}
				addVariant("/music/" + withoutPrefix)
				addVariant("/music/Music/" + withoutPrefix)
			}
		}

		if musicRoot != "" && trimmed != "" {
			addVariant(filepath.Join(musicRoot, trimmed))
			addVariant(filepath.Join(musicRoot, "Music", trimmed))
		}
	}

	return variants
}

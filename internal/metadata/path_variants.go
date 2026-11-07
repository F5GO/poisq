// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл path_variants.go генерирует все допустимые вариации путей к папкам медиатеки,
// разрешая расхождения между базой данных Navidrome и файловой системой:
// - Неразрывные пробелы macOS (NBSP \u00a0, NNBSP \u202f);
// - Суффиксы дубликатов папок (« — копия», « - копия», « (копия)», «_copy», « (1)»);
// - Префиксы корневых папок (/music vs /music/Music vs /music/Music — копия);
// - Различия в тире и дефисах (- vs – vs —) и форме Unicode NFC/NFD.
package metadata

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

var (
	// Регулярное выражение для безотказного удаления любых суффиксов копий,
	// создаваемых macOS Finder, Windows Explorer или Linux:
	// "Music — копия", "Music\u00a0— копия", "Music - копия", "Music (копия)", "Music_copy", "Music (1)"
	copySuffixRegex = regexp.MustCompile(`(?i)[\s\x{00a0}\x{202f}]*([—–\-]|_)?[\s\x{00a0}\x{202f}]*(копия|copy|\(\d+\)|\(\s*копия\s*\)|\(\s*copy\s*\))\s*$`)
)

// NormalizePathSpaces заменяет все типы экзотических пробелов (NBSP \u00a0, тонкие пробелы, идеографические)
// на стандартный ASCII пробел (0x20).
func NormalizePathSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\u00a0' || r == '\u202f' || r == '\u2007' || r == '\u2009' || r == '\u3000' || unicode.Is(unicode.Zs, r) {
			b.WriteRune(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// StripCopySuffix удаляет суффикс дубликата папки из имени сегмента (например, "Music\u00a0— копия" -> "Music").
func StripCopySuffix(segment string) string {
	norm := NormalizePathSpaces(segment)
	cleaned := copySuffixRegex.ReplaceAllString(norm, "")
	return strings.TrimSpace(cleaned)
}

// IsCopyPath проверяет, содержит ли путь признаки папки-дубликата (« — копия», «_copy», « (1)» и др.).
func IsCopyPath(pathStr string) bool {
	norm := strings.ToLower(NormalizePathSpaces(pathStr))
	if strings.Contains(norm, "копия") || strings.Contains(norm, "copy") {
		return true
	}
	parts := strings.Split(norm, string(filepath.Separator))
	for _, part := range parts {
		if copySuffixRegex.MatchString(part) {
			return true
		}
	}
	return false
}

// TrackDedupKey строит нормализованный ключ трека для выявления дубликатов с допуском хронометража.
func TrackDedupKey(artist, title string, durationSec int) string {
	a := strings.ToLower(strings.TrimSpace(NormalizeNFC(NormalizePathSpaces(artist))))
	t := strings.ToLower(strings.TrimSpace(NormalizeNFC(NormalizePathSpaces(title))))
	durBucket := 0
	if durationSec > 0 {
		durBucket = (durationSec + 1) / 4
	}
	return a + "|" + t + fmt.Sprintf("|%d", durBucket)
}

// GeneratePathVariants создает упорядоченный список потенциальных путей к папке на диске,
// перебирая все типовые расхождения структуры каталогов между Navidrome и хостом.
func GeneratePathVariants(musicRoot, targetPath string) []string {
	clean := filepath.Clean(strings.TrimSpace(targetPath))
	if clean == "" || clean == "." || clean == "/" {
		return nil
	}

	variants := make([]string, 0, 32)
	seen := make(map[string]bool)

	addVariant := func(p string) {
		p = filepath.Clean(strings.TrimSpace(p))
		if p != "" && p != "." && p != "/" && !seen[p] {
			seen[p] = true
			variants = append(variants, p)
		}
	}

	// 1. Исходный путь в оригинальном виде
	addVariant(clean)

	// 2. Исходный путь с нормализованными пробелами
	cleanNormSpaces := NormalizePathSpaces(clean)
	addVariant(cleanNormSpaces)

	// 3. Путь с очищенными сегментами от суффиксов «копия»
	cleanStripped := stripCopyFromAllSegments(cleanNormSpaces)
	addVariant(cleanStripped)

	// Если путь относительный — добавляем относительно musicRoot
	if !filepath.IsAbs(clean) && musicRoot != "" {
		addVariant(filepath.Join(musicRoot, clean))
		addVariant(filepath.Join(musicRoot, cleanNormSpaces))
		addVariant(filepath.Join(musicRoot, cleanStripped))
	}

	// 4. Генерация вариантов для различных точек монтирования (/music, /music/Music, /music/Media)
	currentBatch := make([]string, len(variants))
	copy(currentBatch, variants)

	for _, p := range currentBatch {
		// Извлекаем относительную часть относительно корня /music
		trimmed := p
		if strings.HasPrefix(trimmed, "/music/") {
			trimmed = strings.TrimPrefix(trimmed, "/music/")
		} else if strings.HasPrefix(trimmed, "/music") {
			trimmed = strings.TrimPrefix(trimmed, "/music")
		}
		trimmed = strings.TrimPrefix(trimmed, "/")

		// Отделяем префикс папки библиотеки (Music/, Music — копия/, Media/Music/, Audio/)
		for _, prefixToStrip := range []string{
			"Music/", "music/",
			"Music — копия/", "Music\u00a0— копия/", "Music - копия/", "Music – копия/",
			"Media/Music/", "Media/", "Audio/",
		} {
			prefixNorm := NormalizePathSpaces(prefixToStrip)
			trimmedNorm := NormalizePathSpaces(trimmed)
			if strings.HasPrefix(trimmedNorm, prefixNorm) {
				withoutPrefix := strings.TrimPrefix(trimmedNorm, prefixNorm)
				withoutPrefix = strings.TrimPrefix(withoutPrefix, "/")

				addVariant(withoutPrefix)
				if musicRoot != "" {
					// Случай 1: в контейнер смонтирована непосредственно папка Music (контейнер видит /music/<Artist>/<Album>)
					addVariant(filepath.Join(musicRoot, withoutPrefix))
					// Случай 2: в контейнер смонтирована родительская папка Media (контейнер видит /music/Music/<Artist>/<Album>)
					addVariant(filepath.Join(musicRoot, "Music", withoutPrefix))
					// Случай 3: смонтирована папка копии
					addVariant(filepath.Join(musicRoot, "Music — копия", withoutPrefix))
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

func stripCopyFromAllSegments(pathStr string) string {
	parts := strings.Split(pathStr, string(filepath.Separator))
	for i, part := range parts {
		parts[i] = StripCopySuffix(part)
	}
	res := strings.Join(parts, string(filepath.Separator))
	if strings.HasPrefix(pathStr, "/") && !strings.HasPrefix(res, "/") {
		res = "/" + res
	}
	return res
}

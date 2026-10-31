// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл translit.go реализует музыкально-ориентированную фонетическую транслитерацию
// (RU <-> EN), дедупликацию и расширение согласных, а также генерацию поисковых вариантов.
package search

import (
	"strings"
	"unicode"
)

var ruToEnCharMap = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

var enToRuCharMap = map[rune]rune{
	'a': 'а', 'b': 'б', 'c': 'к', 'd': 'д', 'e': 'е', 'f': 'ф', 'g': 'г',
	'h': 'х', 'i': 'и', 'j': 'ж', 'k': 'к', 'l': 'л', 'm': 'м', 'n': 'н',
	'o': 'о', 'p': 'п', 'q': 'к', 'r': 'р', 's': 'с', 't': 'т', 'u': 'у',
	'v': 'в', 'w': 'в', 'x': 'к', 'y': 'и', 'z': 'з',
}

// TransliterateRuToEn переводит русский текст в латиницу с учетом музыкальных и фонетических особенностей.
func TransliterateRuToEn(input string) string {
	lower := strings.ToLower(strings.TrimSpace(input))
	if alias, exists := musicAliases[lower]; exists && !HasCyrillic(alias) {
		return strings.ToLower(alias)
	}

	result := lower
	for _, p := range ruMultiCharPatterns {
		result = strings.ReplaceAll(result, p.pattern, p.en)
	}

	var sb strings.Builder
	sb.Grow(len(result) * 2)

	runes := []rune(result)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if en, ok := ruToEnCharMap[r]; ok {
			sb.WriteString(en)
		} else {
			sb.WriteRune(r)
		}
	}

	return sb.String()
}

// TransliterateEnToRu переводит английский текст в кириллицу с учетом многосимвольных звуков (sh, ch, th, etc.).
func TransliterateEnToRu(input string) string {
	lower := strings.ToLower(strings.TrimSpace(input))
	if alias, exists := reverseMusicAliases[lower]; exists && HasCyrillic(alias) {
		return strings.ToLower(alias)
	}

	result := lower
	for _, p := range enMultiCharPatterns {
		result = strings.ReplaceAll(result, p.pattern, p.ru)
	}

	var sb strings.Builder
	sb.Grow(len(result))

	runes := []rune(result)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		// Особенность английской 'c': перед 'e', 'i', 'y' читается как 'с', иначе как 'к'
		if r == 'c' {
			if i+1 < len(runes) && (runes[i+1] == 'e' || runes[i+1] == 'i' || runes[i+1] == 'y') {
				sb.WriteRune('с')
			} else {
				sb.WriteRune('к')
			}
			continue
		}

		if ru, ok := enToRuCharMap[r]; ok {
			sb.WriteRune(ru)
		} else {
			sb.WriteRune(r)
		}
	}

	return sb.String()
}

// DeduplicateConsonants удаляет повторы одинаковых согласных букв подряд (например, "offspring" -> "ofspring").
func DeduplicateConsonants(input string) string {
	if len(input) <= 1 {
		return input
	}

	runes := []rune(input)
	var sb strings.Builder
	sb.Grow(len(input))

	for i := 0; i < len(runes); i++ {
		if i > 0 && runes[i] == runes[i-1] && isConsonant(runes[i]) {
			continue
		}
		sb.WriteRune(runes[i])
	}

	return sb.String()
}

// DoubleTrailingConsonant дублирует последнюю согласную букву для коротких префиксов (например, "of" -> "off", "оф" -> "офф").
func DoubleTrailingConsonant(input string) string {
	runes := []rune(input)
	if len(runes) < 2 || len(runes) > 5 {
		return input
	}
	last := runes[len(runes)-1]
	if isConsonant(last) && runes[len(runes)-2] != last {
		return input + string(last)
	}
	return input
}

// isConsonant проверяет, является ли буква согласной (для русского и английского алфавитов).
func isConsonant(r rune) bool {
	switch unicode.ToLower(r) {
	case 'b', 'c', 'd', 'f', 'g', 'h', 'j', 'k', 'l', 'm', 'n', 'p', 'q', 'r', 's', 't', 'v', 'w', 'x', 'z',
		'б', 'в', 'г', 'д', 'ж', 'з', 'к', 'л', 'м', 'н', 'п', 'р', 'с', 'т', 'ф', 'х', 'ц', 'ч', 'ш', 'щ':
		return true
	default:
		return false
	}
}

// GenerateVariants возвращает срез всех возможных вариантов строки:
// оригинальная, со сменой раскладки, транслитерированная RU->EN, транслитерированная EN->RU,
// фонетические соответствия гласных/согласных и варианты с нормализацией / дублированием согласных.
func GenerateVariants(query string) []string {
	clean := strings.TrimSpace(query)
	if clean == "" {
		return nil
	}

	lower := strings.ToLower(clean)
	seen := make(map[string]bool)
	var variants []string

	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			variants = append(variants, v)
		}
	}

	add(lower)

	// Проверяем прямые алиасы
	if alias, ok := musicAliases[lower]; ok {
		add(strings.ToLower(alias))
	}
	if alias, ok := reverseMusicAliases[lower]; ok {
		add(strings.ToLower(alias))
	}

	// Смена раскладки QWERTY <-> ЙЦУКЕН
	swapped := strings.ToLower(SwapLayout(clean))
	add(swapped)
	if alias, ok := musicAliases[swapped]; ok {
		add(strings.ToLower(alias))
	}
	if alias, ok := reverseMusicAliases[swapped]; ok {
		add(strings.ToLower(alias))
	}

	// Транслитерация исходного текста
	if HasCyrillic(lower) {
		ru2en := TransliterateRuToEn(lower)
		add(ru2en)
		add(DeduplicateConsonants(ru2en))
		add(DoubleTrailingConsonant(ru2en))
		add(DoubleTrailingConsonant(lower))

		// Английские фонетические соответствия: 'u' <-> 'oo', 'k' <-> 'c' (scoop <-> скуп)
		if strings.Contains(ru2en, "u") {
			add(strings.ReplaceAll(ru2en, "u", "oo"))
		}
		if strings.Contains(ru2en, "k") {
			add(strings.ReplaceAll(ru2en, "k", "c"))
			if strings.Contains(ru2en, "u") {
				add(strings.ReplaceAll(strings.ReplaceAll(ru2en, "u", "oo"), "k", "c"))
			}
		}

		// Дедупликация гласных для запросов вроде "скууп" -> "скуп"
		if strings.Contains(lower, "уу") {
			add(strings.ReplaceAll(lower, "уу", "у"))
			noDoubles := strings.ReplaceAll(lower, "уу", "у")
			add(TransliterateRuToEn(noDoubles))
		}
	}

	if HasLatin(lower) {
		en2ru := TransliterateEnToRu(lower)
		add(en2ru)
		add(DeduplicateConsonants(en2ru))
		add(DoubleTrailingConsonant(en2ru))
		add(DoubleTrailingConsonant(lower))

		// Английские 'oo' <-> 'у', 'c' <-> 'к'
		if strings.Contains(lower, "oo") {
			add(strings.ReplaceAll(lower, "oo", "u"))
		}
	}

	// Транслитерация текста после смены раскладки
	if HasCyrillic(swapped) {
		ru2en := TransliterateRuToEn(swapped)
		add(ru2en)
		add(DeduplicateConsonants(ru2en))
		add(DoubleTrailingConsonant(ru2en))
		add(DoubleTrailingConsonant(swapped))
	}

	if HasLatin(swapped) {
		en2ru := TransliterateEnToRu(swapped)
		add(en2ru)
		add(DeduplicateConsonants(en2ru))
		add(DoubleTrailingConsonant(en2ru))
		add(DoubleTrailingConsonant(swapped))
	}

	// Варианты без артиклей "the" / "зе"
	for _, prefix := range []string{"the ", "зе ", "де "} {
		if strings.HasPrefix(lower, prefix) {
			without := strings.TrimPrefix(lower, prefix)
			add(without)
			add(TransliterateEnToRu(without))
			add(TransliterateRuToEn(without))
		}
	}

	return variants
}

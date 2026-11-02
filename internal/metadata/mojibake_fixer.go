// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл mojibake_fixer.go реализует детектор и конвертер классических кракозябр
// (Windows-1251, прочитанный как Latin-1 / ISO-8859-1) с защитой западноевропейских
// диакритических знаков (Björk, Motörhead) и принудительной нормализацией Unicode в NFC.
package metadata

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Таблица отображения байтов 0x80..0xFF кодировки Windows-1251 в Unicode руны.
var cp1251ToUnicode = [128]rune{
	0x0402, 0x0403, 0x201A, 0x0453, 0x201E, 0x2026, 0x2020, 0x2021,
	0x20AC, 0x2030, 0x0409, 0x2039, 0x040A, 0x040C, 0x040B, 0x040F,
	0x0452, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x0098, 0x2122, 0x0459, 0x203A, 0x045A, 0x045C, 0x045B, 0x045F,
	0x00A0, 0x040E, 0x045E, 0x0408, 0x00A4, 0x0490, 0x00A6, 0x00A7,
	0x0401, 0x00A9, 0x0404, 0x00AB, 0x00AC, 0x00AD, 0x00AE, 0x0407,
	0x00B0, 0x00B1, 0x0406, 0x0456, 0x0491, 0x00B5, 0x00B6, 0x00B7,
	0x0451, 0x2116, 0x0454, 0x00BB, 0x0458, 0x0405, 0x0455, 0x0457,
	// 0xC0..0xDF: А..Я
	'А', 'Б', 'В', 'Г', 'Д', 'Е', 'Ж', 'З', 'И', 'Й', 'К', 'Л', 'М', 'Н', 'О', 'П',
	'Р', 'С', 'Т', 'У', 'Ф', 'Х', 'Ц', 'Ч', 'Ш', 'Щ', 'Ъ', 'Ы', 'Ь', 'Э', 'Ю', 'Я',
	// 0xE0..0xFF: а..я
	'а', 'б', 'в', 'г', 'д', 'е', 'ж', 'з', 'и', 'й', 'к', 'л', 'м', 'н', 'о', 'п',
	'р', 'с', 'т', 'у', 'ф', 'х', 'ц', 'ч', 'ш', 'щ', 'ъ', 'ы', 'ь', 'э', 'ю', 'я',
}

var russianVowels = map[rune]bool{
	'а': true, 'е': true, 'ё': true, 'и': true, 'о': true,
	'у': true, 'ы': true, 'э': true, 'ю': true, 'я': true,
	'А': true, 'Е': true, 'Ё': true, 'И': true, 'О': true,
	'У': true, 'Ы': true, 'Э': true, 'Ю': true, 'Я': true,
}

// NormalizeNFC приводит разложенные символы Unicode NFD (часто встречающиеся в macOS HFS+/APFS)
// в каноническую монолитную форму NFC (например, 'и' + U+0306 -> 'й', 'е' + U+0308 -> 'ё').
func NormalizeNFC(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	var out []rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if i+1 < len(runes) {
			next := runes[i+1]
			if next == 0x0306 { // Combining Breve (кратка)
				switch r {
				case 'и':
					out = append(out, 'й')
					i++
					continue
				case 'И':
					out = append(out, 'Й')
					i++
					continue
				}
			} else if next == 0x0308 { // Combining Diaeresis (умляут / точки над ё)
				switch r {
				case 'е':
					out = append(out, 'ё')
					i++
					continue
				case 'Е':
					out = append(out, 'Ё')
					i++
					continue
				}
			}
		}
		out = append(out, r)
	}
	return string(out)
}

// DetectAndFixMojibake проверяет, является ли строка битой кодировкой CP1251
// (как в форме Latin-1 UTF-8 "Êèíî", так и в форме сырых однобайтовых CP1251 байтов),
// и возвращает исправленную строку в Unicode NFC вместе с флагом обнаружения.
func DetectAndFixMojibake(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", false
	}

	// Форма Б: Сырые не-UTF8 байты Windows-1251
	if !utf8.ValidString(trimmed) {
		var rawDecoded []rune
		highBytes := 0
		for i := 0; i < len(trimmed); i++ {
			b := trimmed[i]
			if b >= 0x80 {
				highBytes++
				rawDecoded = append(rawDecoded, cp1251ToUnicode[b-0x80])
			} else {
				rawDecoded = append(rawDecoded, rune(b))
			}
		}
		cand := NormalizeNFC(string(rawDecoded))
		if highBytes >= 3 && hasValidCyrillicWordStructure(cand) {
			return cand, true
		}
		return cand, false
	}

	normalized := NormalizeNFC(trimmed)

	// Если в строке уже есть нормальная кириллица — перекодировка не нужна
	for _, r := range normalized {
		if unicode.Is(unicode.Cyrillic, r) {
			return normalized, false
		}
	}

	// Форма А: Latin-1 UTF-8 Mojibake (например, "Êèíî - Ãðóïïà êðîâè")
	var highLatinCount int
	var totalLetters int
	var decoded []rune

	for _, r := range normalized {
		if unicode.IsLetter(r) {
			totalLetters++
		}
		if r >= 0x80 && r <= 0xFF {
			if (r >= 0xC0 && r <= 0xFF) || r == 0xA8 || r == 0xB8 {
				highLatinCount++
			}
			decoded = append(decoded, cp1251ToUnicode[r-0x80])
		} else {
			decoded = append(decoded, r)
		}
	}

	// Чтобы не испортить "Björk" или "Motörhead", требуем минимум 3 символа из диапазона кириллицы CP1251
	// и чтобы они составляли не менее 50% всех букв строки
	if highLatinCount < 3 || totalLetters == 0 {
		return normalized, false
	}
	if float64(highLatinCount)/float64(totalLetters) < 0.5 {
		return normalized, false
	}

	candidate := NormalizeNFC(string(decoded))
	if !hasValidCyrillicWordStructure(candidate) {
		return normalized, false
	}

	return candidate, true
}

func hasValidCyrillicWordStructure(candidate string) bool {
	var cyrVowels, cyrConsonants int
	for _, r := range candidate {
		if unicode.Is(unicode.Cyrillic, r) {
			if russianVowels[r] {
				cyrVowels++
			} else {
				cyrConsonants++
			}
		}
	}
	return cyrVowels > 0 && cyrConsonants > 0
}

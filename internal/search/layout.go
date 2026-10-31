// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл layout.go реализует двунаправленную конвертацию клавиатурной
// раскладки между английской QWERTY и русской ЙЦУКЕН, включая пунктуационные символы.
package search

import (
	"strings"
	"unicode"
)

var (
	// enToRuMap содержит сопоставление символов английской раскладки QWERTY в русскую ЙЦУКЕН.
	enToRuMap = map[rune]rune{
		'q': 'й', 'w': 'ц', 'e': 'у', 'r': 'к', 't': 'е', 'y': 'н', 'u': 'г', 'i': 'ш',
		'o': 'щ', 'p': 'з', '[': 'х', ']': 'ъ', 'a': 'ф', 's': 'ы', 'd': 'в', 'f': 'а',
		'g': 'п', 'h': 'р', 'j': 'о', 'k': 'л', 'l': 'д', ';': 'ж', '\'': 'э', 'z': 'я',
		'x': 'ч', 'c': 'с', 'v': 'м', 'b': 'и', 'n': 'т', 'm': 'ь', ',': 'б', '.': 'ю',
		'/': '.', '`': 'ё',

		'Q': 'Й', 'W': 'Ц', 'E': 'У', 'R': 'К', 'T': 'Е', 'Y': 'Н', 'U': 'Г', 'I': 'Ш',
		'O': 'Щ', 'P': 'З', '{': 'Х', '}': 'Ъ', 'A': 'Ф', 'S': 'Ы', 'D': 'В', 'F': 'А',
		'G': 'П', 'H': 'Р', 'J': 'О', 'K': 'Л', 'L': 'Д', ':': 'Ж', '"': 'Э', 'Z': 'Я',
		'X': 'Ч', 'C': 'С', 'V': 'М', 'B': 'И', 'N': 'Т', 'M': 'Ь', '<': 'Б', '>': 'Ю',
		'?': ',', '~': 'Ё', '@': '"', '#': '№', '$': ';', '^': ':', '&': '?',
	}

	// ruToEnMap содержит обратное сопоставление русской раскладки ЙЦУКЕН в английскую QWERTY.
	ruToEnMap = make(map[rune]rune)
)

func init() {
	for en, ru := range enToRuMap {
		ruToEnMap[ru] = en
	}
}

// SwapLayout выполняет смену раскладки текста:
// английские символы переводятся в русские, а русские — в английские.
// Символы, для которых соответствие не задано (пробелы, цифры и т.д.), остаются неизменными.
func SwapLayout(text string) string {
	if text == "" {
		return ""
	}

	var sb strings.Builder
	sb.Grow(len(text))

	for _, r := range text {
		if ru, ok := enToRuMap[r]; ok {
			sb.WriteRune(ru)
		} else if en, ok := ruToEnMap[r]; ok {
			sb.WriteRune(en)
		} else {
			sb.WriteRune(r)
		}
	}

	return sb.String()
}

// HasCyrillic возвращает true, если строка содержит хотя бы одну кириллическую букву.
func HasCyrillic(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// HasLatin возвращает true, если строка содержит хотя бы одну латинскую букву.
func HasLatin(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Latin, r) {
			return true
		}
	}
	return false
}

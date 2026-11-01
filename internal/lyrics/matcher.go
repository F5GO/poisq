// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл matcher.go реализует интеллектуальное сопоставление метаданных треков (исполнитель,
// название) для валидации результатов внешних провайдеров (Genius, Яндекс, Amalgama, LRCLIB).
// Это исключает ложные срабатывания, когда полнотекстовый поиск поисковика выдает совершенно
// другую песню (например, рэп-трек, в тексте которого просто упомянуты искомые слова).
package lyrics

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

var (
	// Регулярные выражения для очистки вспомогательных пометок в названиях
	featRegex   = regexp.MustCompile(`(?i)\s*[\(\[\{]?(?:feat\.?|ft\.?|featuring|prod\.?|remix|version|edit|live|cover)[\s\S]*?[\)\]\}]?$`)
	punctRegex  = regexp.MustCompile(`[^\p{L}\p{N}\s]`)
	spacesRegex = regexp.MustCompile(`\s+`)
)

// NormalizeTrackString нормализует строку для нечеткого сравнения:
// - приводит к нижнему регистру;
// - нормализует смешанную кириллицу/латиницу (например, "МакSим" -> "максим");
// - удаляет знаки препинания и вспомогательные пометки (feat, ft, remix и т.д.);
// - сжимает повторяющиеся пробелы.
func NormalizeTrackString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// 1. Приведение к нижнему регистру
	s = strings.ToLower(s)

	// 2. Нормализация популярных латинских букв в русскоязычных именах (например, МакSим -> максим)
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case 's': // часто используют латинскую 's' вместо кириллической 'с'
			sb.WriteRune('с')
		case 'a':
			sb.WriteRune('а')
		case 'e':
			sb.WriteRune('е')
		case 'o':
			sb.WriteRune('о')
		case 'p':
			sb.WriteRune('р')
		case 'x':
			sb.WriteRune('х')
		case 'c':
			sb.WriteRune('с')
		case 'y':
			sb.WriteRune('у')
		default:
			sb.WriteRune(r)
		}
	}
	s = sb.String()

	// 3. Удаление пометок вида (feat. ...), [Remix] и т.п.
	s = featRegex.ReplaceAllString(s, "")

	// 4. Удаление пунктуации и спецсимволов
	s = punctRegex.ReplaceAllString(s, " ")

	// 5. Схлопывание пробелов
	s = spacesRegex.ReplaceAllString(s, " ")

	return strings.TrimSpace(s)
}

// MatchArtist проверяет совпадение исполнителя с учетом нормализации и взаимных вхождений.
func MatchArtist(queryArtist, candArtist string) bool {
	q := NormalizeTrackString(queryArtist)
	c := NormalizeTrackString(candArtist)

	if q == "" || c == "" {
		return false
	}

	if q == c {
		return true
	}

	// Кандидат начинается или заканчивается искомым артистом (например, "МакСим & Би-2", "Виктор Цой и Кино")
	if strings.HasPrefix(c, q+" ") || strings.HasPrefix(q, c+" ") ||
		strings.HasSuffix(c, " "+q) || strings.HasSuffix(q, " "+c) {
		return true
	}

	// Вхождение как отдельного слова с разделителями
	if strings.Contains(c, " "+q+" ") || strings.Contains(q, " "+c+" ") {
		return true
	}

	return false
}

// MatchTitle проверяет совпадение названия трека с учетом нормализации и вариаций.
func MatchTitle(queryTitle, candTitle string) bool {
	q := NormalizeTrackString(queryTitle)
	c := NormalizeTrackString(candTitle)

	if q == "" || c == "" {
		return false
	}

	if q == c {
		return true
	}

	// Кандидат начинается с искомого названия (например, "отпускаю" -> "отпускаю radio edit")
	if strings.HasPrefix(c, q+" ") || strings.HasPrefix(q, c+" ") {
		return true
	}

	// Проверка совпадения ключевых слов названия (для многословных названий)
	qWords := strings.Fields(q)
	cWords := strings.Fields(c)
	if len(qWords) > 1 && len(cWords) > 1 {
		matchedWords := 0
		for _, qw := range qWords {
			if len(qw) <= 2 {
				continue // пропускаем короткие предлоги
			}
			for _, cw := range cWords {
				if qw == cw {
					matchedWords++
					break
				}
			}
		}
		if matchedWords >= len(qWords)/2+1 && math.Abs(float64(len(qWords)-len(cWords))) <= 2 {
			return true
		}
	}

	return false
}

// MatchTrack производит комплексную валидацию кандидата: совпадение исполнителя и названия.
func MatchTrack(query SearchQuery, candArtist, candTitle string) bool {
	if candArtist != "" && !MatchArtist(query.Artist, candArtist) {
		return false
	}
	if !MatchTitle(query.Title, candTitle) {
		return false
	}
	return true
}

// IsRussianText проверяет, содержит ли текст символы кириллицы.
func IsRussianText(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

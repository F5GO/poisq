// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл lrc_parser.go реализует парсинг и генерацию стандартного формата LRC ([mm:ss.xx]),
// извлечение временных меток в миллисекундах и очистку текста от служебных тегов.
package lyrics

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// Регулярное выражение для поиска временных меток [mm:ss.xx] или [mm:ss.xxx]
	lrcTimeRegex = regexp.MustCompile(`\[(\d{1,2}):(\d{2})(?:\.(\d{2,3}))?\]`)
)

// ParseLRC разбирает сырой текст LRC в слайс структурированных строк Line с расчетом миллисекунд.
func ParseLRC(raw string) []Line {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	lines := strings.Split(raw, "\n")
	var result []Line

	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}

		matches := lrcTimeRegex.FindStringSubmatch(l)
		if len(matches) < 3 {
			continue
		}

		min, _ := strconv.ParseInt(matches[1], 10, 64)
		sec, _ := strconv.ParseInt(matches[2], 10, 64)
		var ms int64
		if len(matches) > 3 && matches[3] != "" {
			fracStr := matches[3]
			if len(fracStr) == 2 {
				fracVal, _ := strconv.ParseInt(fracStr, 10, 64)
				ms = fracVal * 10
			} else if len(fracStr) == 3 {
				ms, _ = strconv.ParseInt(fracStr, 10, 64)
			}
		}

		startMs := min*60*1000 + sec*1000 + ms
		text := lrcTimeRegex.ReplaceAllString(l, "")
		text = strings.TrimSpace(text)

		result = append(result, Line{
			Text:    text,
			StartMs: startMs,
		})
	}

	// Рассчитываем DurationMs как разницу между началом текущей и следующей строки
	for i := 0; i < len(result); i++ {
		if i+1 < len(result) {
			result[i].DurationMs = result[i+1].StartMs - result[i].StartMs
		} else {
			result[i].DurationMs = 3000 // Для последней строки дефолт 3 секунды
		}
	}

	return result
}

// BuildLRC формирует стандартную строку LRC из слайса структурированных строк Line.
func BuildLRC(lines []Line) string {
	if len(lines) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, line := range lines {
		min := line.StartMs / 60000
		sec := (line.StartMs % 60000) / 1000
		hundredths := (line.StartMs % 1000) / 10

		sb.WriteString(fmt.Sprintf("[%02d:%02d.%02d] %s\n", min, sec, hundredths, line.Text))
	}

	return strings.TrimRight(sb.String(), "\n")
}

// StripLRCTimestamps удаляет все временные метки из LRC текста, оставляя чистый Plain текст.
func StripLRCTimestamps(raw string) string {
	cleaned := lrcTimeRegex.ReplaceAllString(raw, "")
	lines := strings.Split(cleaned, "\n")
	var result []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t != "" {
			result = append(result, t)
		}
	}
	return strings.Join(result, "\n")
}

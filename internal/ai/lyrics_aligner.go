// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл lyrics_aligner.go реализует высокоточное пословное выравнивание (Forced Alignment)
// для генерации караоке (.lrc). Он упаковывает пословные DTW-таймкоды Whisper (words) в компактную
// временную шкалу [mm:ss.xx]слово, исключает ложные срабатывания на инструментальных интро/проигрышах
// и вычисляет математические якорные таймкоды для каждой строки эталонного текста.
package ai

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode"
)

// AcousticTimelineData хранит пословные и посегментные акустические данные трека.
type AcousticTimelineData struct {
	Language       string           `json:"language"`
	Duration       float64          `json:"duration"`
	FirstWordStart float64          `json:"firstWordStart"`
	Words          []WhisperWord    `json:"words,omitempty"`
	Segments       []WhisperSegment `json:"segments,omitempty"`
}

// FormatLRCTimestamp переводит секунды (float64) в стандартный тег LRC [mm:ss.xx].
func FormatLRCTimestamp(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	totalCentis := int(math.Round(sec * 100))
	mins := totalCentis / 6000
	rem := totalCentis % 6000
	secs := rem / 100
	centis := rem % 100
	return fmt.Sprintf("[%02d:%02d.%02d]", mins, secs, centis)
}

// BuildAcousticPayload упаковывает результат Groq Whisper в структуру с сохранением всех пословных таймкодов.
func BuildAcousticPayload(res *WhisperTranscriptionResult) (string, error) {
	if res == nil {
		return "", fmt.Errorf("пустой результат транскрибации Whisper")
	}

	firstStart := 0.0
	if len(res.Words) > 0 {
		firstStart = res.Words[0].Start
	} else if len(res.Segments) > 0 {
		firstStart = res.Segments[0].Start
	}

	data := AcousticTimelineData{
		Language:       res.Language,
		Duration:       res.Duration,
		FirstWordStart: firstStart,
		Words:          res.Words,
		Segments:       res.Segments,
	}

	b, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// FormatWordLevelStream превращает массив пословных таймкодов в наглядную и компактную ленту,
// где ПЕРЕД КАЖДЫМ СЛОВОМ стоит его точный акустический таймкод [mm:ss.xx].
// Фразы разделяются переносом строки при паузах в вокале более 0.45 сек.
func FormatWordLevelStream(acousticJSON string) (string, *AcousticTimelineData) {
	var data AcousticTimelineData
	if err := json.Unmarshal([]byte(acousticJSON), &data); err != nil {
		return acousticJSON, nil
	}

	// Если есть точные пословные таймкоды (основной режим высокой точности)
	if len(data.Words) > 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Язык: %s | Длительность: %.1f сек | Начало первого вокала: %s\n\n",
			data.Language, data.Duration, FormatLRCTimestamp(data.FirstWordStart)))

		var lineWords []string
		prevEnd := data.Words[0].Start

		for i, w := range data.Words {
			wordClean := strings.TrimSpace(w.Word)
			if wordClean == "" {
				continue
			}

			gap := w.Start - prevEnd
			// Если пауза между словами > 0.45 сек или накопилось 10 слов — переносим строку для читаемости
			if i > 0 && (gap > 0.45 || len(lineWords) >= 10) {
				sb.WriteString(strings.Join(lineWords, " "))
				sb.WriteString("\n")
				lineWords = lineWords[:0]
			}

			lineWords = append(lineWords, fmt.Sprintf("%s%s", FormatLRCTimestamp(w.Start), wordClean))
			if w.End > w.Start {
				prevEnd = w.End
			} else {
				prevEnd = w.Start + 0.2
			}
		}

		if len(lineWords) > 0 {
			sb.WriteString(strings.Join(lineWords, " "))
			sb.WriteString("\n")
		}

		return sb.String(), &data
	}

	// Фоллбэк на сегменты, если пословный массив отсутствует
	if len(data.Segments) > 0 {
		var sb strings.Builder
		for _, s := range data.Segments {
			sb.WriteString(fmt.Sprintf("%s -> %s %s\n",
				FormatLRCTimestamp(s.Start), FormatLRCTimestamp(s.End), strings.TrimSpace(s.Text)))
		}
		return sb.String(), &data
	}

	return acousticJSON, &data
}

// PreAlignReferenceWithWords выполняет локальное математическое сопоставление (Forced Alignment)
// строк эталонного текста с пословными таймкодами Whisper методом динамического окна и нечеткого поиска.
// Возвращает размеченный эталонный текст с подсказками-якорями для Gemini.
func PreAlignReferenceWithWords(referencePlain string, data *AcousticTimelineData) string {
	refTrimmed := strings.TrimSpace(referencePlain)
	if refTrimmed == "" || data == nil || len(data.Words) == 0 {
		return refTrimmed
	}

	rawLines := strings.Split(refTrimmed, "\n")
	var cleanLines []string
	for _, l := range rawLines {
		t := strings.TrimSpace(l)
		if t != "" {
			cleanLines = append(cleanLines, t)
		}
	}
	if len(cleanLines) == 0 {
		return refTrimmed
	}

	// Нормализуем все слова Whisper для быстрого нечеткого сравнения
	type normWhisperWord struct {
		norm  string
		start float64
		end   float64
	}
	wList := make([]normWhisperWord, 0, len(data.Words))
	for _, w := range data.Words {
		n := normalizeWordForAlign(w.Word)
		if n != "" {
			wList = append(wList, normWhisperWord{norm: n, start: w.Start, end: w.End})
		}
	}
	if len(wList) == 0 {
		return refTrimmed
	}

	var resultBuilder strings.Builder
	searchCursor := 0

	for lineIdx, line := range cleanLines {
		lineTokens := tokenizeLineForAlign(line)
		if len(lineTokens) == 0 {
			resultBuilder.WriteString(fmt.Sprintf("Строка %d: %s\n", lineIdx+1, line))
			continue
		}

		// Определяем окно поиска в массиве слов Whisper
		// Пропорциональная оценка позиции + допуск вперед/назад
		maxWindow := searchCursor + 35
		if maxWindow > len(wList) {
			maxWindow = len(wList)
		}

		bestIdx := -1
		bestScore := 0.0

		for wi := searchCursor; wi < maxWindow; wi++ {
			// Сравниваем первые 1..3 слова строки с позицией wi в Whisper
			matchSum := 0.0
			checkCount := len(lineTokens)
			if checkCount > 3 {
				checkCount = 3
			}
			validChecks := 0
			for k := 0; k < checkCount && (wi+k) < len(wList); k++ {
				sim := wordSimilarity(lineTokens[k], wList[wi+k].norm)
				matchSum += sim
				validChecks++
			}
			if validChecks == 0 {
				continue
			}
			avgSim := matchSum / float64(validChecks)
			// Небольшой штраф за слишком далекий прыжок вперед, чтобы не перескочить куплет
			distancePenalty := float64(wi-searchCursor) * 0.012
			score := avgSim - distancePenalty

			// Первое слово строки должно иметь хорошее фонетическое/буквенное сходство
			firstWordSim := wordSimilarity(lineTokens[0], wList[wi].norm)
			if firstWordSim >= 0.55 && score > bestScore && avgSim >= 0.55 {
				bestScore = score
				bestIdx = wi
			}
		}

		if bestIdx != -1 {
			anchorTime := FormatLRCTimestamp(wList[bestIdx].start)
			resultBuilder.WriteString(fmt.Sprintf("Строка %02d [якорь первого слова ≈ %s]: %s\n", lineIdx+1, anchorTime, line))
			// Сдвигаем курсор поиска минимум на половину длины текущей строки
			advance := len(lineTokens) / 2
			if advance < 1 {
				advance = 1
			}
			searchCursor = bestIdx + advance
			if searchCursor >= len(wList) {
				searchCursor = len(wList) - 1
			}
		} else {
			resultBuilder.WriteString(fmt.Sprintf("Строка %02d [якорь требует уточнения по контексту]: %s\n", lineIdx+1, line))
		}
	}

	return resultBuilder.String()
}

func normalizeWordForAlign(s string) string {
	var runes []rune
	for _, r := range strings.ToLower(s) {
		if r == 'ё' {
			r = 'е'
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			runes = append(runes, r)
		}
	}
	return string(runes)
}

func tokenizeLineForAlign(line string) []string {
	fields := strings.Fields(line)
	var out []string
	for _, f := range fields {
		n := normalizeWordForAlign(f)
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// wordSimilarity возвращает коэффициент схожести двух слов от 0.0 до 1.0 (на базе расстояния Левенштейна).
func wordSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	ra := []rune(a)
	rb := []rune(b)
	la := len(ra)
	lb := len(rb)
	if la == 0 || lb == 0 {
		return 0.0
	}

	// Быстрая матрица Левенштейна
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		copy(prev, curr)
	}

	maxLen := la
	if lb > maxLen {
		maxLen = lb
	}
	return 1.0 - float64(prev[lb])/float64(maxLen)
}

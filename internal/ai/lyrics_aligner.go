// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл lyrics_aligner.go реализует калибровку пословных DTW-таймкодов Whisper:
// 1. Очищает транскрипцию от галлюцинаций Whisper на тишине (субтитры, копирайты).
// 2. Устраняет классический дефект DTW в Whisper на музыке (Word Duration Clamping из stable-ts):
//    когда первое слово после инструментального интро или проигрыша растягивает свой Start
//    назад к началу музыки (например, start=0.00, end=15.40), алгоритм вычисляет истинный момент
//    вступления голоса от точной правой границы слова (end - naturalDuration).
// 3. Группирует откалиброванные слова в естественные вокальные фразы караоке с точными таймкодами.
package ai

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode"
)

// AcousticPhrase представляет одну реально пропетую вокальную строчку (фразу),
// собранную из откалиброванных слов и сегментов Whisper.
type AcousticPhrase struct {
	Index int           `json:"index"`
	Start float64       `json:"start"`
	End   float64       `json:"end"`
	Text  string        `json:"text"`
	Words []WhisperWord `json:"words,omitempty"`
}

// AcousticTimelineData хранит откалиброванные пословные и посегментные акустические данные трека.
type AcousticTimelineData struct {
	Language       string           `json:"language"`
	Duration       float64          `json:"duration"`
	FirstWordStart float64          `json:"firstWordStart"`
	Words          []WhisperWord    `json:"words,omitempty"`
	Segments       []WhisperSegment `json:"segments,omitempty"`
	Phrases        []AcousticPhrase `json:"phrases,omitempty"`
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

// isWhisperHallucination фильтрует известные паразитные фразы Whisper на инструменталах и тишине.
func isWhisperHallucination(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return true
	}
	hallucinations := []string{
		"субтитры сделал",
		"субтитры создавал",
		"редактор субтитров",
		"корректор",
		"dimatorzok",
		"продолжение следует",
		"amara.org",
		"подпишись на канал",
		"ставьте лайки",
		"thank you for watching",
		"subtitles by",
	}
	for _, h := range hallucinations {
		if strings.Contains(lower, h) {
			return true
		}
	}
	return false
}

// naturalWordDuration оценивает естественную длительность пропевания одного слова по числу букв.
func naturalWordDuration(word string) (natDur float64, maxDur float64) {
	runeLen := len([]rune(normalizeWordForAlign(word)))
	if runeLen <= 0 {
		runeLen = 2
	}
	natDur = float64(runeLen) * 0.085
	if natDur < 0.28 {
		natDur = 0.28
	}
	if natDur > 0.75 {
		natDur = 0.75
	}

	maxDur = float64(runeLen) * 0.14
	if maxDur < 0.55 {
		maxDur = 0.55
	}
	if maxDur > 1.35 {
		maxDur = 1.35
	}
	return natDur, maxDur
}

// SanitizeWhisperTimeline очищает и калибрует сырой ответ Whisper:
// удаляет галлюцинации и исправляет растянутые назад таймкоды Start у слов после интро и проигрышей.
func SanitizeWhisperTimeline(res *WhisperTranscriptionResult) *AcousticTimelineData {
	if res == nil {
		return &AcousticTimelineData{}
	}

	// 1. Очищаем сегменты от галлюцинаций тишины
	cleanSegments := make([]WhisperSegment, 0, len(res.Segments))
	for _, seg := range res.Segments {
		txt := strings.TrimSpace(seg.Text)
		if isWhisperHallucination(txt) {
			continue
		}
		cleanSegments = append(cleanSegments, WhisperSegment{
			ID:    seg.ID,
			Start: seg.Start,
			End:   seg.End,
			Text:  txt,
		})
	}

	// 2. Очищаем и калибруем слова (Word Duration Clamping)
	cleanWords := make([]WhisperWord, 0, len(res.Words))
	prevEnd := 0.0

	for i, w := range res.Words {
		wText := strings.TrimSpace(w.Word)
		if wText == "" || isWhisperHallucination(wText) {
			continue
		}
		// Пропускаем чисто пунктуационные токены без букв/цифр
		if normalizeWordForAlign(wText) == "" {
			continue
		}

		start := w.Start
		end := w.End
		if end < start {
			end = start
		}

		natDur, maxDur := naturalWordDuration(wText)

		// Проверяем, есть ли сразу после этого слова следующее слово впритык
		if i+1 < len(res.Words) && res.Words[i+1].Start > start && res.Words[i+1].Start < end {
			end = res.Words[i+1].Start
		}

		// КЛЮЧЕВОЕ ИСПРАВЛЕНИЕ (stable-ts clamping):
		// Если длительность слова (end - start) превышает maxDur (например, start=0.00, end=15.20 на интро),
		// значит DTW-матрица Whisper притянула start к началу инструментального проигрыша!
		// Истинное начало слова отсчитывается назад от точной правой границы end.
		if end-start > maxDur {
			clampedStart := end - natDur
			if clampedStart < prevEnd {
				clampedStart = prevEnd
			}
			start = clampedStart
		}

		if start < prevEnd {
			start = prevEnd
		}
		if end <= start {
			end = start + 0.18
		}

		cleanWords = append(cleanWords, WhisperWord{
			Word:  wText,
			Start: math.Round(start*100) / 100,
			End:   math.Round(end*100) / 100,
		})
		prevEnd = end
	}

	// 3. Синхронизируем Start каждого сегмента с первым калиброванным словом внутри него
	if len(cleanWords) > 0 && len(cleanSegments) > 0 {
		for si := range cleanSegments {
			seg := &cleanSegments[si]
			for _, w := range cleanWords {
				if w.End > seg.Start+0.05 && w.Start < seg.End+0.5 {
					if w.Start > seg.Start {
						seg.Start = w.Start
					}
					break
				}
			}
		}
	}

	firstStart := 0.0
	if len(cleanWords) > 0 {
		firstStart = cleanWords[0].Start
	} else if len(cleanSegments) > 0 {
		firstStart = cleanSegments[0].Start
	}

	data := &AcousticTimelineData{
		Language:       res.Language,
		Duration:       res.Duration,
		FirstWordStart: firstStart,
		Words:          cleanWords,
		Segments:       cleanSegments,
	}
	data.Phrases = BuildAcousticPhrases(data)
	return data
}

// BuildAcousticPhrases группирует откалиброванные слова Whisper в естественные вокальные строки караоке,
// опираясь на границы сегментов Whisper и дыхательные паузы вокалиста.
func BuildAcousticPhrases(data *AcousticTimelineData) []AcousticPhrase {
	if data == nil {
		return nil
	}

	// Если есть пословные таймкоды — строим фразы с точностью до первого слова каждой фразы
	if len(data.Words) > 0 {
		// Собираем границы концов сегментов Whisper как подсказки естественного конца фразы
		segEnds := make([]float64, 0, len(data.Segments))
		for _, s := range data.Segments {
			segEnds = append(segEnds, s.End)
		}

		isNearSegmentEnd := func(wordEnd float64) bool {
			for _, se := range segEnds {
				if math.Abs(wordEnd-se) <= 0.22 {
					return true
				}
			}
			return false
		}

		var phrases []AcousticPhrase
		var curWords []WhisperWord

		flushPhrase := func() {
			if len(curWords) == 0 {
				return
			}
			var texts []string
			for _, cw := range curWords {
				texts = append(texts, strings.TrimSpace(cw.Word))
			}
			phrases = append(phrases, AcousticPhrase{
				Index: len(phrases) + 1,
				Start: curWords[0].Start,
				End:   curWords[len(curWords)-1].End,
				Text:  strings.Join(texts, " "),
				Words: append([]WhisperWord(nil), curWords...),
			})
			curWords = curWords[:0]
		}

		for i, w := range data.Words {
			if len(curWords) > 0 {
				prevW := curWords[len(curWords)-1]
				gap := w.Start - prevW.End
				// Разрываем строку, если:
				// 1) Пауза между словами >= 0.45 сек (вдох певца или проигрыш);
				// 2) Предыдущее слово совпало с концом сегмента Whisper и в строке уже >= 3 слов;
				// 3) В строке накопилось >= 7 слов и есть микропауза >= 0.18 сек (или >= 9 слов жестко).
				if gap >= 0.45 ||
					(len(curWords) >= 3 && isNearSegmentEnd(prevW.End)) ||
					(len(curWords) >= 7 && gap >= 0.18) ||
					len(curWords) >= 9 {
					flushPhrase()
				}
			}
			curWords = append(curWords, w)
			if i == len(data.Words)-1 {
				flushPhrase()
			}
		}

		return phrases
	}

	// Фоллбэк на сегменты, если массив слов пуст
	var phrases []AcousticPhrase
	for i, s := range data.Segments {
		phrases = append(phrases, AcousticPhrase{
			Index: i + 1,
			Start: s.Start,
			End:   s.End,
			Text:  strings.TrimSpace(s.Text),
		})
	}
	return phrases
}

// BuildAcousticPayload упаковывает результат Groq Whisper в JSON с предварительной калибровкой таймкодов.
func BuildAcousticPayload(res *WhisperTranscriptionResult) (string, error) {
	if res == nil {
		return "", fmt.Errorf("пустой результат транскрибации Whisper")
	}

	data := SanitizeWhisperTimeline(res)
	b, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// FormatWordLevelStream превращает откалиброванные данные Whisper в двойное представление:
// 1) Готовые акустические фразы [mm:ss.xx] (где таймкод каждой фразы равен точному началу её первого слова);
// 2) Пословную ленту [mm:ss.xx]слово для сверки каждого отдельного слова.
func FormatWordLevelStream(acousticJSON string) (string, *AcousticTimelineData) {
	var data AcousticTimelineData
	if err := json.Unmarshal([]byte(acousticJSON), &data); err != nil {
		return acousticJSON, nil
	}

	// На случай если JSON был прочитан из кэша без предварительной калибровки фраз
	if len(data.Words) > 0 && len(data.Phrases) == 0 {
		sanitized := SanitizeWhisperTimeline(&WhisperTranscriptionResult{
			Language: data.Language,
			Duration: data.Duration,
			Words:    data.Words,
			Segments: data.Segments,
		})
		data = *sanitized
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Язык: %s | Длительность: %.1f сек | Вступление первого слова вокала: %s\n\n",
		data.Language, data.Duration, FormatLRCTimestamp(data.FirstWordStart)))

	if len(data.Phrases) > 0 {
		sb.WriteString("--- АКУСТИЧЕСКИЕ ВОКАЛЬНЫЕ ФРАЗЫ (ТОЧНЫЙ ТАЙМИНГ ПЕРВОГО СЛОВА КАЖДОЙ ФРАЗЫ) ---\n")
		for _, p := range data.Phrases {
			sb.WriteString(fmt.Sprintf("%s %s\n", FormatLRCTimestamp(p.Start), p.Text))
		}
		sb.WriteString("\n")
	}

	if len(data.Words) > 0 {
		sb.WriteString("--- ДЕТАЛЬНАЯ ПОСЛОВНАЯ СЕТКА ([mm:ss.xx]слово) ---\n")
		for _, p := range data.Phrases {
			if len(p.Words) == 0 {
				continue
			}
			var lineTokens []string
			for _, w := range p.Words {
				lineTokens = append(lineTokens, fmt.Sprintf("%s%s", FormatLRCTimestamp(w.Start), strings.TrimSpace(w.Word)))
			}
			sb.WriteString(strings.Join(lineTokens, " "))
			sb.WriteString("\n")
		}
		return sb.String(), &data
	}

	return sb.String(), &data
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
	dist := prev[lb]
	sim := 1.0 - (float64(dist) / float64(maxLen))
	if sim < 0 {
		return 0
	}
	return sim
}

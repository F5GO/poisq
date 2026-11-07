// Package ai реализует облачные AI-пайплайны сервиса poisq: мультиязычную очистку
// и консенсус текстов через Gemini, сжатие аудио на лету в моно-Opus
// и двухэтапную генерацию синхронизированного караоке через Groq Whisper + Gemini.
//
// Что делает этот код и для чего он нужен:
// Данный файл needleman_aligner.go реализует глобальное динамическое программирование
// (алгоритм Нидлмана — Вунша / Needleman-Wunsch Sequence Alignment) для 100% детерминированной
// привязки строк текста песни к пословным таймкодам аудиозаписи от Whisper, а также
// пост-валидацию (SnapLRCToAcousticWordsStats), которая исправляет любые сдвиги таймкодов от LLM.
package ai

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/F5GO/poisq/internal/logger"
)

type refTokenPos struct {
	norm      string
	lineIdx   int
	wordInLin int
}

// PreAlignReferenceWithWords выполняет глобальное выравнивание последовательностей Нидлмана — Вунша
// между всеми словами эталонного текста песни и всеми откалиброванными словами Whisper.
// Возвращает готовый синхронизированный LRC-текст с математически выверенными таймкодами [mm:ss.xx].
func PreAlignReferenceWithWords(referencePlain string, data *AcousticTimelineData) string {
	refTrimmed := strings.TrimSpace(referencePlain)
	if refTrimmed == "" || data == nil || len(data.Words) == 0 {
		return refTrimmed
	}

	rawLines := strings.Split(refTrimmed, "\n")
	var cleanLines []string
	var lineTokensList [][]string

	for _, l := range rawLines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		toks := tokenizeLineForAlign(t)
		if len(toks) == 0 {
			continue
		}
		cleanLines = append(cleanLines, t)
		lineTokensList = append(lineTokensList, toks)
	}

	if len(cleanLines) == 0 {
		return refTrimmed
	}

	lineTimes := computeGlobalDPLineTimestamps(lineTokensList, data)

	var sb strings.Builder
	for i, line := range cleanLines {
		sb.WriteString(fmt.Sprintf("%s %s\n", FormatLRCTimestamp(lineTimes[i]), line))
	}
	return strings.TrimSpace(sb.String())
}

// computeGlobalDPLineTimestamps вычисляет точный акустический таймкод начала каждой строки
// при помощи глобальной матрицы динамического программирования (Needleman-Wunsch).
func computeGlobalDPLineTimestamps(lineTokensList [][]string, data *AcousticTimelineData) []float64 {
	times, _ := computeGlobalDPLineTimestampsDetailed(lineTokensList, data)
	return times
}

// computeGlobalDPLineTimestampsDetailed дополнительно возвращает флаги: какие строки реально
// сопоставлены со словами Whisper (true), а какие получили интерполированное время (false).
func computeGlobalDPLineTimestampsDetailed(lineTokensList [][]string, data *AcousticTimelineData) ([]float64, []bool) {
	numLines := len(lineTokensList)
	lineTimes := make([]float64, numLines)
	lineMatched := make([]bool, numLines)

	// Собираем плоский список эталонных слов всей песни
	var refTokens []refTokenPos
	for li, toks := range lineTokensList {
		for wi, tok := range toks {
			refTokens = append(refTokens, refTokenPos{
				norm:      tok,
				lineIdx:   li,
				wordInLin: wi,
			})
		}
	}

	// Собираем нормализованный список слов Whisper
	type normW struct {
		norm  string
		start float64
		end   float64
	}
	wList := make([]normW, 0, len(data.Words))
	for _, w := range data.Words {
		n := normalizeWordForAlign(w.Word)
		if n != "" {
			wList = append(wList, normW{norm: n, start: w.Start, end: w.End})
		}
	}

	P := len(refTokens)
	Q := len(wList)
	if P == 0 || Q == 0 {
		return lineTimes, lineMatched
	}

	// Матрица глобального выравнивания Нидлмана — Вунша размера (P+1) x (Q+1)
	const (
		dirDiag = 1
		dirUp   = 2
		dirLeft = 3
	)
	dp := make([][]float32, P+1)
	dir := make([][]uint8, P+1)
	for i := 0; i <= P; i++ {
		dp[i] = make([]float32, Q+1)
		dir[i] = make([]uint8, Q+1)
	}

	const gapRef float32 = -0.95
	const gapWhisper float32 = -0.65

	for i := 1; i <= P; i++ {
		dp[i][0] = float32(i) * gapRef
		dir[i][0] = dirUp
	}
	for j := 1; j <= Q; j++ {
		// Мягкий штраф за лишние слова в начале аудио
		dp[0][j] = float32(j) * (-0.30)
		dir[0][j] = dirLeft
	}

	for i := 1; i <= P; i++ {
		rNorm := refTokens[i-1].norm
		isFirstInLine := refTokens[i-1].wordInLin == 0
		for j := 1; j <= Q; j++ {
			sim := float32(wordSimilarity(rNorm, wList[j-1].norm))
			var matchScore float32
			if sim >= 0.50 {
				matchScore = 3.2 * sim
				// Дополнительный бонус за совпадение первого слова строки
				if isFirstInLine && sim >= 0.70 {
					matchScore += 0.45
				}
			} else {
				matchScore = -0.85
			}

			scoreDiag := dp[i-1][j-1] + matchScore
			scoreUp := dp[i-1][j] + gapRef
			scoreLeft := dp[i][j-1] + gapWhisper

			best := scoreDiag
			bestDir := uint8(dirDiag)
			if scoreUp > best {
				best = scoreUp
				bestDir = dirUp
			}
			if scoreLeft > best {
				best = scoreLeft
				bestDir = dirLeft
			}

			dp[i][j] = best
			dir[i][j] = bestDir
		}
	}

	// Обратный проход (Backtracking) для извлечения глобально оптимального соответствия слов
	matchedWhisperIdx := make([]int, P)
	for i := range matchedWhisperIdx {
		matchedWhisperIdx[i] = -1
	}

	i, j := P, Q
	for i > 0 && j > 0 {
		d := dir[i][j]
		if d == dirDiag {
			sim := wordSimilarity(refTokens[i-1].norm, wList[j-1].norm)
			if sim >= 0.48 {
				matchedWhisperIdx[i-1] = j - 1
			}
			i--
			j--
		} else if d == dirUp {
			i--
		} else {
			j--
		}
	}

	// Вычисляем точное время начала каждой строки по сопоставленным словам Whisper
	tokenOffset := 0
	for li := 0; li < numLines; li++ {
		tokCount := len(lineTokensList[li])
		// Ищем первое сматченное слово в этой строке
		for k := 0; k < tokCount; k++ {
			wIdx := matchedWhisperIdx[tokenOffset+k]
			if wIdx >= 0 {
				if k == 0 {
					// Прямое попадание в первое слово строки!
					lineTimes[li] = wList[wIdx].start
				} else if wIdx-k >= 0 {
					// Первое слово строки было ослышано Whisper'ом, но k-е слово совпало:
					// берем реальное слово на k позиций левее (если оно не залезло на предыдущую строку)
					candStart := wList[wIdx-k].start
					minAllowed := wList[wIdx].start - float64(k)*0.55
					if candStart < minAllowed {
						candStart = minAllowed
					}
					if candStart < data.FirstWordStart {
						candStart = data.FirstWordStart
					}
					lineTimes[li] = candStart
				} else {
					est := wList[wIdx].start - float64(k)*0.32
					if est < data.FirstWordStart {
						est = data.FirstWordStart
					}
					lineTimes[li] = est
				}
				lineMatched[li] = true
				break
			}
		}
		tokenOffset += tokCount
	}

	matchedTokens := 0
	for _, idx := range matchedWhisperIdx {
		if idx >= 0 {
			matchedTokens++
		}
	}
	matchedLines := 0
	for _, m := range lineMatched {
		if m {
			matchedLines++
		}
	}

	// Интерполируем строки, в которых Whisper не расслышал ни одного слова (например, бэк-вокал)
	interpolateUnmatchedLines(lineTimes, lineMatched, data)

	logger.Info("[ALIGN] DP-выравнивание: слов эталона %d (сматчено %d, %.0f%%), слов Whisper %d, строк %d (по голосу %d, ИНТЕРПОЛИРОВАНО %d)",
		P, matchedTokens, 100*float64(matchedTokens)/float64(P), Q, numLines, matchedLines, numLines-matchedLines)
	if numLines-matchedLines > 0 {
		for li, m := range lineMatched {
			if !m {
				logger.Debug("[ALIGN] Строка %d без совпадений с Whisper, время интерполировано: %s", li+1, FormatLRCTimestamp(lineTimes[li]))
			}
		}
	}
	return lineTimes, lineMatched
}

func interpolateUnmatchedLines(lineTimes []float64, lineMatched []bool, data *AcousticTimelineData) {
	n := len(lineTimes)
	if n == 0 {
		return
	}

	minStart := 0.0
	maxEnd := 180.0
	if data != nil {
		minStart = data.FirstWordStart
		if data.Duration > minStart {
			maxEnd = data.Duration
		} else if len(data.Words) > 0 {
			maxEnd = data.Words[len(data.Words)-1].End + 2.0
		}
	}

	for i := 0; i < n; i++ {
		if lineMatched[i] {
			continue
		}
		// Ищем ближайшую привязанную строку слева и справа
		prevIdx := -1
		for p := i - 1; p >= 0; p-- {
			if lineMatched[p] {
				prevIdx = p
				break
			}
		}
		nextIdx := -1
		for nx := i + 1; nx < n; nx++ {
			if lineMatched[nx] {
				nextIdx = nx
				break
			}
		}

		if prevIdx == -1 && nextIdx != -1 {
			// В начале песни до первой сматченной строки (но никогда не раньше FirstWordStart!)
			steps := float64(nextIdx - i)
			est := lineTimes[nextIdx] - steps*2.2
			if est < minStart {
				est = minStart + float64(i)*1.2
			}
			lineTimes[i] = est
		} else if prevIdx != -1 && nextIdx != -1 {
			// Линейная интерполяция внутри окна между двумя известными строками
			span := lineTimes[nextIdx] - lineTimes[prevIdx]
			if span < 0.5 {
				span = 0.5
			}
			fraction := float64(i-prevIdx) / float64(nextIdx-prevIdx)
			lineTimes[i] = lineTimes[prevIdx] + span*fraction
		} else if prevIdx != -1 && nextIdx == -1 {
			steps := float64(i - prevIdx)
			est := lineTimes[prevIdx] + steps*2.5
			if est > maxEnd {
				est = maxEnd
			}
			lineTimes[i] = est
		} else {
			lineTimes[i] = minStart + float64(i)*3.0
		}
	}

	// Гарантируем строгую монотонность и защиту от старта раньше первого слова вокала
	for i := 0; i < n; i++ {
		if lineTimes[i] < minStart {
			lineTimes[i] = minStart
		}
		if i > 0 && lineTimes[i] <= lineTimes[i-1] {
			lineTimes[i] = math.Round((lineTimes[i-1]+0.25)*100) / 100
		}
	}
}

var lrcLineParseRe = regexp.MustCompile(`^\s*\[(\d{1,2}):(\d{2})(?:\.(\d{1,3}))?\]\s*(.+)$`)

// SnapStats описывает, насколько результат подтверждён реальным звуком.
type SnapStats struct {
	Lines       int // всего строк
	Unconfirmed int // строки, время которых НЕ подтверждено ни одним словом Whisper (угадано/интерполировано)
}

// SnapLRCToAcousticWordsStats выравнивает строки LRC по словам Whisper, прижимает таймкоды к началу слов и возвращает статистику
// достоверности: сколько строк получили время без подтверждения голосом.
func SnapLRCToAcousticWordsStats(lrcText string, data *AcousticTimelineData) (string, SnapStats) {
	var stats SnapStats
	trimmed := strings.TrimSpace(lrcText)
	if trimmed == "" || data == nil || len(data.Words) == 0 {
		return trimmed, stats
	}

	type parsedLine struct {
		sec    float64
		text   string
		tokens []string
	}

	rawLines := strings.Split(trimmed, "\n")
	var parsed []parsedLine
	var tokenMatrix [][]string

	for _, raw := range rawLines {
		m := lrcLineParseRe.FindStringSubmatch(strings.TrimSpace(raw))
		if len(m) == 5 {
			mins, _ := strconv.Atoi(m[1])
			secs, _ := strconv.Atoi(m[2])
			msStr := m[3]
			for len(msStr) < 3 {
				msStr += "0"
			}
			if len(msStr) > 3 {
				msStr = msStr[:3]
			}
			ms, _ := strconv.Atoi(msStr)
			totalSec := float64(mins*60+secs) + float64(ms)/1000.0
			txt := strings.TrimSpace(m[4])
			toks := tokenizeLineForAlign(txt)
			if txt != "" && len(toks) > 0 {
				parsed = append(parsed, parsedLine{sec: totalSec, text: txt, tokens: toks})
				tokenMatrix = append(tokenMatrix, toks)
			}
		}
	}

	if len(parsed) == 0 {
		return trimmed, stats
	}

	// Вычисляем акустические таймкоды строк через глобальный алгоритм Нидлмана — Вунша
	dpTimes, dpMatched := computeGlobalDPLineTimestampsDetailed(tokenMatrix, data)

	// Начала всех слов Whisper — «физические якоря» в аудио
	wordStarts := make([]float64, 0, len(data.Words))
	for _, w := range data.Words {
		wordStarts = append(wordStarts, w.Start)
	}
	nearestStart := func(t float64) (float64, float64) {
		best, bestDist := t, math.MaxFloat64
		for _, ws := range wordStarts {
			if d := math.Abs(ws - t); d < bestDist {
				best, bestDist = ws, d
			}
		}
		return best, bestDist
	}

	// Если DP не нашёл строку в голосе (интерполяция) или разошёлся с Gemini, а время Gemini
	// попадает в начало реального слова Whisper (±1 сек) — доверяем Gemini: интерполяция
	// заведомо хуже таймкода, подтверждённого словом в аудио.
	const trustWindow = 1.0

	var sb strings.Builder
	prevSec := -1.0
	shifted := 0
	maxShift := 0.0
	usedGemini := 0
	for i, pl := range parsed {
		d := dpTimes[i]
		g := pl.sec
		gSnap, gDist := nearestStart(g)

		var finalSec float64
		switch {
		case dpMatched[i] && math.Abs(d-g) <= trustWindow:
			finalSec = d
		case gDist <= trustWindow:
			finalSec = gSnap
			if !dpMatched[i] || math.Abs(d-g) > trustWindow {
				usedGemini++
			}
		case dpMatched[i]:
			finalSec = d
		default:
			stats.Unconfirmed++
			finalSec = g
			if finalSec < d && d > 0 && g <= 0 {
				finalSec = d
			}
		}

		if finalSec < data.FirstWordStart {
			finalSec = data.FirstWordStart
		}
		if prevSec >= 0 && finalSec <= prevSec {
			finalSec = math.Round((prevSec+0.25)*100) / 100
		}
		prevSec = finalSec
		if dd := math.Abs(finalSec - g); dd > 0.5 {
			shifted++
			if dd > maxShift {
				maxShift = dd
			}
			logger.Debug("[ALIGN] Snap строки %d: Gemini %s → итог %s (Δ %.2f сек, DP %s, по голосу=%v)",
				i+1, FormatLRCTimestamp(g), FormatLRCTimestamp(finalSec), dd, FormatLRCTimestamp(d), dpMatched[i])
		}
		sb.WriteString(fmt.Sprintf("%s %s\n", FormatLRCTimestamp(finalSec), pl.text))
	}
	logger.Info("[ALIGN] Snap после Gemini: строк %d, таймкод изменён более чем на 0.5 сек у %d (макс. сдвиг %.2f сек), оставлено время Gemini, подтверждённое словом Whisper: %d",
		len(parsed), shifted, maxShift, usedGemini)

	stats.Lines = len(parsed)
	logger.Info("[ALIGN] Достоверность: строк без подтверждения голосом %d из %d", stats.Unconfirmed, stats.Lines)
	return strings.TrimSpace(sb.String()), stats
}

// BuildDirectAcousticLRC формирует чистый LRC напрямую из откалиброванных акустических фраз Whisper.
func BuildDirectAcousticLRC(data *AcousticTimelineData) string {
	if data == nil {
		return ""
	}
	phrases := data.Phrases
	if len(phrases) == 0 {
		phrases = BuildAcousticPhrases(data)
	}
	var sb strings.Builder
	for _, p := range phrases {
		txt := strings.TrimSpace(p.Text)
		if txt == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("%s %s\n", FormatLRCTimestamp(p.Start), txt))
	}
	return strings.TrimSpace(sb.String())
}

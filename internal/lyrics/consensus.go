// Package lyrics предоставляет систему поиска, ранжирования и сохранения текстов песен (Lyrics).
//
// Данный файл consensus.go реализует выбор победителя турнира «по большинству»:
//  1. Тексты всех кандидатов приводятся к последовательности слов (без таймкодов, пометок [Куплет], скобок).
//  2. Кандидаты, чьи тексты совпадают друг с другом на 80% и более (коэффициент Дайса по словам), образуют кластер.
//  3. Побеждает самый большой кластер (минимум 2 независимых источника).
//  4. Внутри кластера берётся «полноценный» LRC: достаточно строк, таймкоды растут, последний — внутри трека.
//
// Так мы не принимаем синхронный текст от источника, который отдал другую песню или другую редакцию,
// и не отбрасываем верный LRC только из-за расхождения в написании исполнителя.
package lyrics

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/F5GO/poisq/internal/logger"
)

const (
	// consensusAgreeThreshold — минимальная похожесть текстов (Дайс по словам), чтобы считать их «одним текстом».
	consensusAgreeThreshold = 0.80
	// consensusNearThreshold — «близкий вариант» (другая редакция): допускается для LRC вне кластера.
	consensusNearThreshold = 0.60
	// minVoteTokens — тексты короче не участвуют в голосовании (пустышки, инструментал).
	minVoteTokens = 20
)

var (
	bracketTagRe = regexp.MustCompile(`\[[^\]]*\]`)
	parenTextRe  = regexp.MustCompile(`\([^)]*\)`)
	lrcInlineRe  = regexp.MustCompile(`<\d{1,2}:\d{2}(?:\.\d{1,3})?>`)
)

// ConsensusInfo описывает результат голосования для логов и UI.
type ConsensusInfo struct {
	Voters      int      `json:"voters"`      // сколько кандидатов с текстом голосовало
	Agreeing    int      `json:"agreeing"`    // сколько из них в победившем кластере
	Providers   []string `json:"providers"`   // источники победившего кластера
	Similarity  float64  `json:"similarity"`  // средняя похожесть внутри кластера
	HasFullLRC  bool     `json:"hasFullLrc"`  // найден ли полноценный LRC в кластере
	Reason      string   `json:"reason"`      // человекочитаемое обоснование выбора
	Dissenters  []string `json:"dissenters"`  // источники, чей текст не совпал с большинством
	NearSynced  string   `json:"nearSynced"`  // источник LRC «близкой редакции», если взят он
	ConsensusOK bool     `json:"consensusOk"` // true, если есть согласие минимум двух источников
}

type voter struct {
	idx    int
	cand   TournamentCandidate
	tokens []string
	counts map[string]int
}

// textTokens приводит текст кандидата (LRC или обычный) к списку нормализованных слов.
func textTokens(l *Lyrics) []string {
	if l == nil {
		return nil
	}
	raw := l.PlainLyrics
	if strings.TrimSpace(raw) == "" {
		raw = l.SyncedLyrics
	}
	raw = StripLRCTimestamps(raw)
	raw = lrcInlineRe.ReplaceAllString(raw, " ")
	raw = bracketTagRe.ReplaceAllString(raw, " ")
	raw = parenTextRe.ReplaceAllString(raw, " ")

	var tokens []string
	for _, line := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(line)
		low := strings.ToLower(t)
		// Служебный мусор Genius и подобных страниц
		if strings.HasSuffix(low, " lyrics") || strings.Contains(low, "you might also like") ||
			strings.Contains(low, "contributors") || low == "embed" || strings.HasSuffix(low, "embed") {
			continue
		}
		for _, w := range strings.FieldsFunc(low, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			w = strings.ReplaceAll(w, "ё", "е")
			if w != "" {
				tokens = append(tokens, w)
			}
		}
	}
	return tokens
}

func bagCounts(tokens []string) map[string]int {
	m := make(map[string]int, len(tokens))
	for _, t := range tokens {
		m[t]++
	}
	return m
}

// diceSimilarity — коэффициент Дайса для мультимножеств слов (не зависит от порядка и повторов припевов).
func diceSimilarity(a, b map[string]int, la, lb int) float64 {
	if la == 0 || lb == 0 {
		return 0
	}
	inter := 0
	for w, ca := range a {
		if cb, ok := b[w]; ok {
			if ca < cb {
				inter += ca
			} else {
				inter += cb
			}
		}
	}
	return 2 * float64(inter) / float64(la+lb)
}

// nonEmptyLineCount считает непустые строки текста без таймкодов.
func nonEmptyLineCount(l *Lyrics) int {
	raw := l.PlainLyrics
	if strings.TrimSpace(raw) == "" {
		raw = l.SyncedLyrics
	}
	raw = StripLRCTimestamps(raw)
	n := 0
	for _, line := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(bracketTagRe.ReplaceAllString(line, ""))
		if t != "" {
			n++
		}
	}
	return n
}

// IsFullLRC проверяет, что синхронный текст «полноценный»: достаточно строк по сравнению с эталоном,
// таймкоды не убывают, последний таймкод лежит внутри трека и не слишком рано.
func IsFullLRC(l *Lyrics, refLines int, trackDuration time.Duration) (bool, string) {
	if l == nil || strings.TrimSpace(l.SyncedLyrics) == "" {
		return false, "нет синхронного текста"
	}
	lines := ParseLRC(l.SyncedLyrics)
	minLines := 8
	if want := int(math.Round(0.7 * float64(refLines))); want > minLines {
		minLines = want
	}
	if len(lines) < minLines {
		return false, fmt.Sprintf("мало строк: %d < %d", len(lines), minLines)
	}
	prev := int64(-1)
	for _, ln := range lines {
		if ln.StartMs < prev {
			return false, "таймкоды идут не по возрастанию"
		}
		prev = ln.StartMs
	}
	last := time.Duration(lines[len(lines)-1].StartMs) * time.Millisecond
	if trackDuration > 0 {
		if last > trackDuration+10*time.Second {
			return false, fmt.Sprintf("последний таймкод %s за пределами трека %s", last.Round(time.Second), trackDuration.Round(time.Second))
		}
		if last < time.Duration(0.35*float64(trackDuration)) {
			return false, fmt.Sprintf("LRC обрывается на %s из %s", last.Round(time.Second), trackDuration.Round(time.Second))
		}
	}
	return true, ""
}

func medianInt(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	c := append([]int(nil), xs...)
	sort.Ints(c)
	return c[len(c)/2]
}

// PickConsensusWinner выбирает победителя «по большинству». Возвращает nil, если согласия нет
// (меньше двух источников с текстом или тексты не совпадают) — тогда используется прежний выбор по баллам.
func PickConsensusWinner(cands []TournamentCandidate, q SearchQuery) (*TournamentCandidate, *ConsensusInfo) {
	var voters []voter
	for i, c := range cands {
		if c.Lyrics == nil || c.Lyrics.Instrumental {
			continue
		}
		toks := textTokens(c.Lyrics)
		if len(toks) < minVoteTokens {
			logger.Info("[CONSENSUS] %s не участвует в голосовании: в тексте всего %d слов (нужно ≥ %d)", c.ProviderID, len(toks), minVoteTokens)
			continue
		}
		voters = append(voters, voter{idx: i, cand: c, tokens: toks, counts: bagCounts(toks)})
	}
	if len(voters) < 2 {
		return nil, &ConsensusInfo{Voters: len(voters), Reason: "недостаточно источников с текстом для голосования"}
	}

	n := len(voters)
	sim := make([][]float64, n)
	for i := range sim {
		sim[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		sim[i][i] = 1
		for j := i + 1; j < n; j++ {
			s := diceSimilarity(voters[i].counts, voters[j].counts, len(voters[i].tokens), len(voters[j].tokens))
			sim[i][j], sim[j][i] = s, s
		}
	}

	// Центр кластера — кандидат, с которым согласно больше всего других источников
	bestCenter, bestSupport := -1, -1
	bestScore := -1.0
	for i := 0; i < n; i++ {
		support := 0
		for j := 0; j < n; j++ {
			if sim[i][j] >= consensusAgreeThreshold {
				support++
			}
		}
		if support > bestSupport || (support == bestSupport && voters[i].cand.Score.Total > bestScore) {
			bestCenter, bestSupport, bestScore = i, support, voters[i].cand.Score.Total
		}
	}

	var cluster []int
	var dissenters []string
	for j := 0; j < n; j++ {
		if sim[bestCenter][j] >= consensusAgreeThreshold {
			cluster = append(cluster, j)
		} else {
			dissenters = append(dissenters, voters[j].cand.ProviderID)
		}
	}

	info := &ConsensusInfo{Voters: n, Agreeing: len(cluster), Dissenters: dissenters}
	for _, j := range cluster {
		info.Providers = append(info.Providers, voters[j].cand.ProviderID)
	}
	if len(cluster) < 2 {
		info.Reason = "источники не согласны друг с другом: ни один текст не подтверждён вторым источником"
		return nil, info
	}
	info.ConsensusOK = true

	total, cnt := 0.0, 0
	for _, a := range cluster {
		for _, b := range cluster {
			if a < b {
				total += sim[a][b]
				cnt++
			}
		}
	}
	if cnt > 0 {
		info.Similarity = total / float64(cnt)
	}

	var lineCounts []int
	for _, j := range cluster {
		lineCounts = append(lineCounts, nonEmptyLineCount(voters[j].cand.Lyrics))
	}
	refLines := medianInt(lineCounts)

	// Лучший полноценный LRC внутри кластера
	var bestLRC *voter
	var rejected []string
	for _, j := range cluster {
		v := &voters[j]
		ok, why := IsFullLRC(v.cand.Lyrics, refLines, q.Duration)
		if !ok {
			if strings.TrimSpace(v.cand.Lyrics.SyncedLyrics) != "" {
				rejected = append(rejected, fmt.Sprintf("%s: %s", v.cand.ProviderID, why))
			}
			continue
		}
		if bestLRC == nil || v.cand.Score.Total > bestLRC.cand.Score.Total ||
			(v.cand.Score.Total == bestLRC.cand.Score.Total && len(v.cand.Lyrics.Lines) > len(bestLRC.cand.Lyrics.Lines)) {
			bestLRC = v
		}
	}
	if bestLRC != nil {
		info.HasFullLRC = true
		info.Reason = fmt.Sprintf("%d из %d источников (%s) согласны по тексту (сходство %.0f%%); взят полноценный LRC от %s",
			len(cluster), n, strings.Join(info.Providers, ", "), info.Similarity*100, bestLRC.cand.ProviderID)
		w := bestLRC.cand
		return &w, info
	}

	// В кластере нет полноценного LRC: ищем LRC «близкой редакции» среди остальных (сходство с центром >= 0.60)
	var near *voter
	for j := 0; j < n; j++ {
		if sim[bestCenter][j] >= consensusAgreeThreshold {
			continue
		}
		if sim[bestCenter][j] < consensusNearThreshold {
			continue
		}
		v := &voters[j]
		if ok, _ := IsFullLRC(v.cand.Lyrics, refLines, q.Duration); ok {
			if near == nil || v.cand.Score.Total > near.cand.Score.Total {
				near = v
			}
		}
	}
	if near != nil {
		info.HasFullLRC = true
		info.NearSynced = near.cand.ProviderID
		info.Reason = fmt.Sprintf("в большинстве (%s) нет полноценного LRC; взят LRC близкой редакции от %s (сходство с большинством %.0f%%)",
			strings.Join(info.Providers, ", "), near.cand.ProviderID, sim[bestCenter][near.idxInVoters(voters)]*100)
		w := near.cand
		return &w, info
	}

	// Нет подходящего LRC: победитель — лучший по баллу текст большинства (станет эталоном для генерации караоке)
	best := cluster[0]
	for _, j := range cluster {
		if voters[j].cand.Score.Total > voters[best].cand.Score.Total {
			best = j
		}
	}
	info.Reason = fmt.Sprintf("%d из %d источников (%s) согласны по тексту (сходство %.0f%%), но полноценного LRC нет",
		len(cluster), n, strings.Join(info.Providers, ", "), info.Similarity*100)
	if len(rejected) > 0 {
		info.Reason += "; отклонённые LRC: " + strings.Join(rejected, "; ")
	}
	w := voters[best].cand
	return &w, info
}

func (v *voter) idxInVoters(all []voter) int {
	for i := range all {
		if all[i].idx == v.idx {
			return i
		}
	}
	return 0
}

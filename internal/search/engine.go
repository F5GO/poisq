// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл engine.go реализует основной конвейер интеллектуального поиска:
// двухуровневый анализ (префиксный автокомплит для 1-3 рун и нечеткий поиск для 4+ рун),
// разрешение опечаток, привязку к родительской сущности и параллельный fan-out.
package search

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// SearchResult содержит результат работы интеллектуального переписывания запроса.
type SearchResult struct {
	RawQuery         string       `json:"raw_query"`
	RewrittenQuery   string       `json:"rewritten_query"`
	AlternateQuery   string       `json:"alternate_query,omitempty"`
	CandidateQueries []string     `json:"candidate_queries,omitempty"`
	NeedsFanout      bool         `json:"needs_fanout"`
	Variants         []string     `json:"variants"`
	DurationMicros   int64        `json:"duration_micros"`
	Matches          []MatchToken `json:"matched_tokens,omitempty"`
}

// MatchToken описывает сопоставленный токен из словаря библиотеки.
type MatchToken struct {
	Original  string `json:"original"`
	Canonical string `json:"canonical"`
	Distance  int    `json:"distance"`
	Weight    int    `json:"weight"`
}

// Engine инкапсулирует словарь, кэш запросов и логику интеллектуальной обработки поиска.
type Engine struct {
	dict       *Dictionary
	cacheMu    sync.RWMutex
	queryCache map[string]SearchResult
}

// NewEngine создает новый экземпляр движка поиска.
func NewEngine(dict *Dictionary) *Engine {
	return &Engine{
		dict:       dict,
		queryCache: make(map[string]SearchResult, 2048),
	}
}

// ProcessQuery выполняет анализ и переписывание поискового запроса за доли миллисекунды.
func (e *Engine) ProcessQuery(rawQuery string) SearchResult {
	start := time.Now()
	clean := strings.TrimSpace(rawQuery)

	if clean == "" {
		return SearchResult{
			RawQuery:       rawQuery,
			RewrittenQuery: rawQuery,
			Variants:       GenerateVariants(clean),
			DurationMicros: time.Since(start).Microseconds(),
		}
	}

	// Проверяем сверхбыстрый кэш в оперативной памяти (0.0001ms)
	lowerClean := strings.ToLower(clean)
	e.cacheMu.RLock()
	cached, ok := e.queryCache[lowerClean]
	e.cacheMu.RUnlock()
	if ok {
		cached.RawQuery = rawQuery
		cached.DurationMicros = time.Since(start).Microseconds()
		return cached
	}

	res := SearchResult{
		RawQuery:       rawQuery,
		RewrittenQuery: rawQuery,
		Variants:       GenerateVariants(clean),
	}

	runeLen := utf8.RuneCountInString(clean)

	// Уровень 1: Короткие запросы (1-3 символа) — режим префиксного автокомплита
	if runeLen <= 3 {
		e.processShortQuery(&res, clean)
	} else {
		// Уровень 2: Длинные запросы (4+ символов) — режим быстрого Trie-префикса и нечеткого поиска
		e.processLongQuery(&res, clean, runeLen)
	}

	res.DurationMicros = time.Since(start).Microseconds()

	// Сохраняем результат в кэше движка
	e.cacheMu.Lock()
	if len(e.queryCache) >= 4096 {
		e.queryCache = make(map[string]SearchResult, 2048)
	}
	e.queryCache[lowerClean] = res
	e.cacheMu.Unlock()

	return res
}

// processShortQuery обрабатывает запросы от 1 до 3 рун (автокомплит по префиксу).
func (e *Engine) processShortQuery(res *SearchResult, clean string) {
	type candidate struct {
		target  string // ParentEntity если заполнена, иначе Canonical
		weight  int
		variant string
	}

	var candidates []candidate
	seenTarget := make(map[string]bool)
	lowerClean := strings.ToLower(clean)
	isCyrillicQuery := HasCyrillic(clean)
	isLatinQuery := HasLatin(clean)

	// 1. Поиск по префиксному дереву для всех сгенерированных вариантов
	for _, v := range res.Variants {
		matches := e.dict.SearchPrefix(v, 5)
		for _, m := range matches {
			target := m.Canonical
			if m.ParentEntity != "" {
				target = m.ParentEntity
			}

			if !seenTarget[target] {
				seenTarget[target] = true
				weight := m.Weight

				// Приоритет совпадениям в исходном алфавите пользователя
				if (isCyrillicQuery && HasCyrillic(v)) || (isLatinQuery && HasLatin(v)) || v == lowerClean {
					weight += 1000
				}

				candidates = append(candidates, candidate{
					target:  target,
					weight:  weight,
					variant: v,
				})
			}
		}
	}

	// 2. Если по Trie ничего не найдено, проверяем нечеткий префикс с расстоянием 1
	if len(candidates) == 0 {
		for _, v := range res.Variants {
			if entry, dist, found := e.dict.FindFuzzyPrefix(v, 1); found {
				target := entry.Canonical
				if entry.ParentEntity != "" {
					target = entry.ParentEntity
				}
				if !seenTarget[target] {
					seenTarget[target] = true
					weight := entry.Weight - dist*50
					if (isCyrillicQuery && HasCyrillic(v)) || (isLatinQuery && HasLatin(v)) || v == lowerClean {
						weight += 1000
					}
					candidates = append(candidates, candidate{
						target:  target,
						weight:  weight,
						variant: v,
					})
				}
			}
		}
	}

	if len(candidates) == 0 {
		// Если ничего не найдено в словаре, проверяем смену раскладки
		swapped := SwapLayout(clean)
		if strings.ToLower(swapped) != strings.ToLower(clean) {
			res.RewrittenQuery = clean
			res.AlternateQuery = swapped
			res.NeedsFanout = true
		}
		return
	}

	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.weight > best.weight {
			best = c
		}
	}

	// Переписываем запрос на каноническое название сущности в Navidrome
	res.RewrittenQuery = best.target
	res.AlternateQuery = ComputeAlternateQuery(clean, best.target)
	res.NeedsFanout = (clean != best.target)

	res.Matches = append(res.Matches, MatchToken{
		Original:  clean,
		Canonical: best.target,
		Distance:  0,
		Weight:    best.weight,
	})
}

// processLongQuery обрабатывает запросы длиной 4+ рун с исправлением опечаток, префиксов и раскладки.
func (e *Engine) processLongQuery(res *SearchResult, clean string, runeLen int) {
	type candidate struct {
		target  string
		weight  int
		variant string
	}

	var fastCandidates []candidate
	seenTarget := make(map[string]bool)
	lowerClean := strings.ToLower(clean)
	isCyrillicQuery := HasCyrillic(clean)
	isLatinQuery := HasLatin(clean)

	// Быстрый этап 1: O(L) поиск по Trie и точному словарю для всех вариантов (<0.01ms)
	for _, v := range res.Variants {
		if entry, ok := e.dict.FindExact(v); ok {
			target := entry.Canonical
			if entry.ParentEntity != "" {
				target = entry.ParentEntity
			}
			if !seenTarget[target] {
				seenTarget[target] = true
				weight := entry.Weight + 2000
				if (isCyrillicQuery && HasCyrillic(v)) || (isLatinQuery && HasLatin(v)) || v == lowerClean {
					weight += 1000
				}
				fastCandidates = append(fastCandidates, candidate{target: target, weight: weight, variant: v})
			}
		}

		matches := e.dict.SearchPrefix(v, 5)
		for _, m := range matches {
			target := m.Canonical
			if m.ParentEntity != "" {
				target = m.ParentEntity
			}
			if !seenTarget[target] {
				seenTarget[target] = true
				weight := m.Weight
				if (isCyrillicQuery && HasCyrillic(v)) || (isLatinQuery && HasLatin(v)) || v == lowerClean {
					weight += 1000
				}
				fastCandidates = append(fastCandidates, candidate{target: target, weight: weight, variant: v})
			}
		}
	}

	if len(fastCandidates) > 0 {
		best := fastCandidates[0]
		for _, c := range fastCandidates[1:] {
			if c.weight > best.weight {
				best = c
			}
		}

		seenCand := make(map[string]bool)
		for _, c := range fastCandidates {
			if !seenCand[c.target] {
				seenCand[c.target] = true
				res.CandidateQueries = append(res.CandidateQueries, c.target)
				if len(res.CandidateQueries) >= 4 {
					break
				}
			}
		}

		res.RewrittenQuery = best.target
		res.AlternateQuery = ComputeAlternateQuery(clean, best.target)
		res.NeedsFanout = (clean != best.target || len(res.CandidateQueries) > 1)
		res.Matches = append(res.Matches, MatchToken{
			Original:  clean,
			Canonical: best.target,
			Distance:  0,
			Weight:    best.weight,
		})
		return
	}

	maxDist := AllowedMaxDistance(runeLen)

	var bestEntry TokenEntry
	bestDist := maxDist + 1
	bestWeight := -1

	// 1. Проверяем полное нечеткое совпадение всей фразы и ее вариантов
	for _, v := range res.Variants {
		vRuneLen := utf8.RuneCountInString(v)
		vMaxDist := AllowedMaxDistance(vRuneLen)

		// Сначала ищем точное или близкое совпадение токена целиком
		if entry, dist, found := e.dict.FindFuzzy(v, vMaxDist); found {
			if dist < bestDist || (dist == bestDist && entry.Weight > bestWeight) {
				bestDist = dist
				bestEntry = entry
				bestWeight = entry.Weight
			}
		}

		// Также проверяем нечеткий префикс (например, "короы" -> "король")
		if entry, dist, found := e.dict.FindFuzzyPrefix(v, vMaxDist); found {
			if dist < bestDist || (dist == bestDist && entry.Weight > bestWeight) {
				bestDist = dist
				bestEntry = entry
				bestWeight = entry.Weight
			}
		}
	}

	// Если найдено уверенное совпадение
	if bestDist <= maxDist && bestWeight != -1 {
		target := bestEntry.Canonical
		if bestEntry.ParentEntity != "" {
			target = bestEntry.ParentEntity
		}

		res.RewrittenQuery = target
		res.AlternateQuery = ComputeAlternateQuery(clean, target)
		res.NeedsFanout = (clean != target)

		res.Matches = append(res.Matches, MatchToken{
			Original:  clean,
			Canonical: target,
			Distance:  bestDist,
			Weight:    bestWeight,
		})
		return
	}

	// 2. Если фраза состоит из нескольких слов, сначала проверяем голосование токенов за общую сущность
	words := strings.Fields(clean)
	if len(words) > 1 {
		if matchedEntity, phraseCandidates, found := MatchPhraseTokens(clean, e.dict); found {
			res.RewrittenQuery = matchedEntity
			res.CandidateQueries = phraseCandidates
			res.AlternateQuery = ComputeAlternateQuery(clean, matchedEntity)
			res.NeedsFanout = true
			res.Matches = append(res.Matches, MatchToken{
				Original:  clean,
				Canonical: matchedEntity,
				Distance:  0,
				Weight:    1500,
			})
			return
		}

		var rewrittenWords []string
		anyRewritten := false

		for _, w := range words {
			wVariants := GenerateVariants(w)
			wRuneLen := utf8.RuneCountInString(w)
			wMaxDist := AllowedMaxDistance(wRuneLen)

			wBestWord := w
			wBestDist := wMaxDist + 1
			wBestWeight := -1

			for _, wv := range wVariants {
				if entry, dist, found := e.dict.FindFuzzy(wv, wMaxDist); found {
					if dist < wBestDist || (dist == wBestDist && entry.Weight > wBestWeight) {
						wBestDist = dist
						wBestWord = entry.Canonical
						wBestWeight = entry.Weight
					}
				}
				if entry, dist, found := e.dict.FindFuzzyPrefix(wv, wMaxDist); found {
					if dist < wBestDist || (dist == wBestDist && entry.Weight > wBestWeight) {
						wBestDist = dist
						wBestWord = entry.Canonical
						wBestWeight = entry.Weight
					}
				}
			}

			if wBestDist <= wMaxDist && wBestWord != w {
				anyRewritten = true
				res.Matches = append(res.Matches, MatchToken{
					Original:  w,
					Canonical: wBestWord,
					Distance:  wBestDist,
					Weight:    wBestWeight,
				})
			}
			rewrittenWords = append(rewrittenWords, wBestWord)
		}

		if anyRewritten {
			joined := strings.Join(rewrittenWords, " ")
			res.RewrittenQuery = joined
			res.AlternateQuery = ComputeAlternateQuery(clean, joined)
			res.NeedsFanout = true
			return
		}
	}

	// 3. Если слова не найдены в словаре, используем прямое переключение раскладки
	swapped := SwapLayout(clean)
	if strings.ToLower(swapped) != strings.ToLower(clean) {
		res.RewrittenQuery = clean
		res.AlternateQuery = swapped
		res.NeedsFanout = true
	}
}

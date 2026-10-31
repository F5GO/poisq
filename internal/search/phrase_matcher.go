// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл phrase_matcher.go реализует контекстное сопоставление многословных поисковых запросов:
// голосование токенов за общую родительскую сущность (ParentEntity) и ранжирование кандидатов
// для предотвращения склеивания разнородных слов в фиктивные фразы.
package search

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// PhraseCandidate описывает кандидата, найденного по отдельным словам фразы.
type PhraseCandidate struct {
	Entity string
	Votes  int
	Weight int
}

// MatchPhraseTokens анализирует слова поисковой фразы и выявляет общую родительскую сущность в библиотеке.
// Если 2 или более слов запроса указывают на одну и ту же сущность (например, "cut" и "bridge" -> "Cut the Bridge"),
// эта сущность выбирается победителем вместо разрозненных случайных слов.
func MatchPhraseTokens(phrase string, dict *Dictionary) (string, []string, bool) {
	words := strings.Fields(phrase)
	if len(words) < 2 {
		return "", nil, false
	}

	entityVotes := make(map[string]int)
	entityWeights := make(map[string]int)

	for _, w := range words {
		wClean := strings.Trim(w, ",.-_!?'\"()[]{}")
		wLower := strings.ToLower(wClean)
		if utf8.RuneCountInString(wClean) < 2 || stopWords[wLower] {
			continue
		}

		variants := GenerateVariants(wClean)
		wRuneLen := utf8.RuneCountInString(wClean)
		wMaxDist := AllowedMaxDistance(wRuneLen)

		seenForThisWord := make(map[string]bool)

		for _, wv := range variants {
			// 1. Точный поиск токена
			if entry, ok := dict.FindExact(wv); ok {
				target := entry.Canonical
				if entry.ParentEntity != "" {
					target = entry.ParentEntity
				}
				if !seenForThisWord[target] {
					seenForThisWord[target] = true
					entityVotes[target]++
					if entry.Weight > entityWeights[target] {
						entityWeights[target] = entry.Weight
					}
				}
			}

			// 2. Префиксный поиск токена
			matches := dict.SearchPrefix(wv, 3)
			for _, m := range matches {
				target := m.Canonical
				if m.ParentEntity != "" {
					target = m.ParentEntity
				}
				if !seenForThisWord[target] {
					seenForThisWord[target] = true
					entityVotes[target]++
					if m.Weight > entityWeights[target] {
						entityWeights[target] = m.Weight
					}
				}
			}

			// 3. Нечеткий поиск токена с расстоянием Левенштейна
			if entry, dist, found := dict.FindFuzzy(wv, wMaxDist); found && dist <= 1 {
				target := entry.Canonical
				if entry.ParentEntity != "" {
					target = entry.ParentEntity
				}
				if !seenForThisWord[target] {
					seenForThisWord[target] = true
					entityVotes[target]++
					if entry.Weight > entityWeights[target] {
						entityWeights[target] = entry.Weight
					}
				}
			}
		}
	}

	if len(entityVotes) == 0 {
		return "", nil, false
	}

	var candidates []PhraseCandidate
	for ent, votes := range entityVotes {
		candidates = append(candidates, PhraseCandidate{
			Entity: ent,
			Votes:  votes,
			Weight: entityWeights[ent] + votes*500,
		})
	}

	// Сортируем кандидатов: сначала по количеству совпавших слов (голосов), затем по весу
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Votes != candidates[j].Votes {
			return candidates[i].Votes > candidates[j].Votes
		}
		return candidates[i].Weight > candidates[j].Weight
	})

	var candidateList []string
	for _, c := range candidates {
		candidateList = append(candidateList, c.Entity)
		if len(candidateList) >= 4 {
			break
		}
	}

	// Если лучший кандидат набрал 2+ голоса или поддержан большинством слов
	best := candidates[0]
	if best.Votes >= 2 || (len(words) == 2 && best.Votes >= 1) {
		return best.Entity, candidateList, true
	}

	return best.Entity, candidateList, false
}

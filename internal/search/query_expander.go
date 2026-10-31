// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл query_expander.go формирует расширенный набор поисковых запросов для параллельного fan-out:
// каноническое имя, исходный запрос, запрос без стоп-слов и альтернативные кандидаты медиатеки.
package search

import (
	"strings"
	"unicode/utf8"
)

// ExpandQuery строит оптимизированный список от 1 до 4 параллельных запросов для Navidrome.
// Это позволяет находить музыку не только по одному строгому совпадению, но и по смежным
// релевантным токенам (например, "Cut the Bridge", "cut the bridge", "cut bridge", "bridge").
func ExpandQuery(rawQuery string, res *SearchResult) []string {
	cleanRaw := strings.TrimSpace(rawQuery)
	if cleanRaw == "" {
		return nil
	}

	seen := make(map[string]bool)
	var queries []string

	addQuery := func(q string) {
		trimmed := strings.TrimSpace(q)
		if trimmed == "" {
			return
		}
		lower := strings.ToLower(trimmed)
		if !seen[lower] {
			seen[lower] = true
			queries = append(queries, trimmed)
		}
	}

	// 1. Приоритетный канонический запрос (исправленный рерайт)
	if res != nil && res.RewrittenQuery != "" {
		addQuery(res.RewrittenQuery)
	}

	// 2. Исходный запрос пользователя (как ввел пользователь)
	addQuery(cleanRaw)

	// 3. Запрос без стоп-слов (the, a, on, in, etc.), если они присутствуют
	words := strings.Fields(cleanRaw)
	if len(words) > 1 {
		var meaningfulWords []string
		hasStopWord := false
		for _, w := range words {
			wClean := strings.Trim(w, ",.-_!?'\"()[]{}")
			wLower := strings.ToLower(wClean)
			if stopWords[wLower] {
				hasStopWord = true
			} else if utf8.RuneCountInString(wClean) >= 2 {
				meaningfulWords = append(meaningfulWords, wClean)
			}
		}

		if hasStopWord && len(meaningfulWords) >= 1 {
			addQuery(strings.Join(meaningfulWords, " "))
		}
	}

	// 4. Альтернативный запрос (транслит / альтернативная раскладка)
	if res != nil && res.AlternateQuery != "" {
		addQuery(res.AlternateQuery)
	}

	// 5. Кандидаты из библиотеки (смежные треки / артисты)
	if res != nil && len(res.CandidateQueries) > 0 {
		for _, cand := range res.CandidateQueries {
			if len(queries) >= 4 {
				break
			}
			addQuery(cand)
		}
	}

	// Ограничиваем максимальное количество параллельных запросов до 4
	if len(queries) > 4 {
		queries = queries[:4]
	}

	return queries
}

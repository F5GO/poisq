// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл query_expander_test.go тестирует расширение поисковых запросов
// для широкого и точного параллельного опроса Navidrome.
package search

import (
	"strings"
	"testing"
)

func TestExpandQuery(t *testing.T) {
	res := &SearchResult{
		RawQuery:         "cut the bridge",
		RewrittenQuery:   "Cut the Bridge",
		AlternateQuery:   "cut bridge",
		CandidateQueries: []string{"Under the Bridge", "London Bridge"},
	}

	queries := ExpandQuery("cut the bridge", res)
	if len(queries) < 2 {
		t.Fatalf("ExpandQuery вернул слишком мало запросов: %v", queries)
	}

	// Проверяем наличие ключевых вариантов
	hasRewritten := false
	hasRaw := false
	hasNoStop := false

	for _, q := range queries {
		if strings.EqualFold(q, "Cut the Bridge") {
			hasRewritten = true
		}
		if strings.EqualFold(q, "cut the bridge") {
			hasRaw = true
		}
		if strings.EqualFold(q, "cut bridge") {
			hasNoStop = true
		}
	}

	if !hasRewritten {
		t.Errorf("Ожидалось наличие переписанного запроса 'Cut the Bridge' в %v", queries)
	}
	if !hasRaw && !hasRewritten {
		t.Errorf("Ожидалось наличие исходного запроса в %v", queries)
	}
	if !hasNoStop {
		t.Errorf("Ожидалось наличие запроса без стоп-слов 'cut bridge' в %v", queries)
	}
}

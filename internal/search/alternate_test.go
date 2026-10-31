// Package search реализует интеллектуальный поиск, смену раскладки,
// транслитерацию и нечеткое сопоставление поисковых запросов для Navidrome.
//
// Данный файл alternate_test.go тестирует корректность формирования альтернативных
// поисковых запросов для параллельного fan-out (латиница в кириллицу, кириллица в латиницу, раскладка).
package search

import (
	"testing"
)

func TestComputeAlternateQuery(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		rewritten string
		expected  string
	}{
		{
			name:      "Latin to Cyrillic entity: Na moey -> На Моей Луне",
			raw:       "Na moey",
			rewritten: "На Моей Луне",
			expected:  "на моей",
		},
		{
			name:      "Latin to Cyrillic entity: Lune -> На Моей Луне",
			raw:       "Lune",
			rewritten: "На Моей Луне",
			expected:  "луне",
		},
		{
			name:      "Cyrillic to Latin entity: Нике -> Nickelback",
			raw:       "Нике",
			rewritten: "Nickelback",
			expected:  "nike",
		},
		{
			name:      "Cyrillic to Latin entity: Линкин -> Linkin Park",
			raw:       "Линкин",
			rewritten: "Linkin Park",
			expected:  "linkin",
		},
		{
			name:      "Wrong keyboard layout: rbyj -> Кино",
			raw:       "rbyj",
			rewritten: "Кино",
			expected:  "rbyj", // swapped "кино" совпадает с rewritten, отдаем raw
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeAlternateQuery(tt.raw, tt.rewritten)
			if got != tt.expected {
				t.Errorf("ComputeAlternateQuery(%q, %q) = %q, want %q", tt.raw, tt.rewritten, got, tt.expected)
			}
		})
	}
}

// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл client_adapter_test.go тестирует декорацию названий для мобильных клиентов Subsonic.
package proxy

import (
	"testing"
)

func TestDecorateEntityName(t *testing.T) {
	tests := []struct {
		name       string
		entityName string
		rawQuery   string
		expected   string
	}{
		{
			name:       "Exact match substring - no decoration",
			entityName: "Green Day",
			rawQuery:   "green",
			expected:   "Green Day",
		},
		{
			name:       "Latin to Cyrillic: Na moey -> На Моей Луне",
			entityName: "На Моей Луне",
			rawQuery:   "Na moey",
			expected:   "На Моей Луне (Na moey)",
		},
		{
			name:       "Latin to Cyrillic: Lune -> На Моей Луне",
			entityName: "На Моей Луне",
			rawQuery:   "Lune",
			expected:   "На Моей Луне (Lune)",
		},
		{
			name:       "Cyrillic to Latin: Нике -> Nickelback",
			entityName: "Nickelback",
			rawQuery:   "Нике",
			expected:   "Nickelback (Нике)",
		},
		{
			name:       "Empty raw query",
			entityName: "The Offspring",
			rawQuery:   "",
			expected:   "The Offspring",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecorateEntityName(tt.entityName, tt.rawQuery)
			if got != tt.expected {
				t.Errorf("DecorateEntityName(%q, %q) = %q, want %q", tt.entityName, tt.rawQuery, got, tt.expected)
			}
		})
	}
}

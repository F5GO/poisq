// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл scanner_arbiter_test.go тестирует детекцию синглов и спорных папок (IsSuspectedSingleOrDisputed),
// а также логику взаимодействия сканера с интеллектуальным арбитром метаданных.
package metadata

import (
	"testing"

	"github.com/F5GO/poisq/internal/db"
)

func TestIsSuspectedSingleOrDisputed(t *testing.T) {
	tests := []struct {
		name       string
		folderPath string
		tracks     []db.TrackRecord
		expected   bool
	}{
		{
			name:       "Single track folder",
			folderPath: "/music/Green Day/Boulevard of Broken Dreams",
			tracks: []db.TrackRecord{
				{Title: "Boulevard of Broken Dreams", Artist: "Green Day", Album: "Boulevard of Broken Dreams"},
			},
			expected: true,
		},
		{
			name:       "Folder name contains hyphen separator",
			folderPath: "/music/Music/Green Day/Green Day – Boulevard of Broken Dreams",
			tracks: []db.TrackRecord{
				{Title: "Boulevard of Broken Dreams", Artist: "Green Day"},
				{Title: "Extra Track", Artist: "Green Day"},
				{Title: "Live Track", Artist: "Green Day"},
			},
			expected: true,
		},
		{
			name:       "Full standard numbered album",
			folderPath: "/music/Music/Green Day/American Idiot",
			tracks: []db.TrackRecord{
				{Title: "American Idiot", TrackNumber: 1, Album: "American Idiot"},
				{Title: "Jesus of Suburbia", TrackNumber: 2, Album: "American Idiot"},
				{Title: "Holiday", TrackNumber: 3, Album: "American Idiot"},
				{Title: "Boulevard of Broken Dreams", TrackNumber: 4, Album: "American Idiot"},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := IsSuspectedSingleOrDisputed(tt.folderPath, tt.tracks)
			if res != tt.expected {
				t.Errorf("IsSuspectedSingleOrDisputed() = %v, expected %v", res, tt.expected)
			}
		})
	}
}

// Package db отвечает за безопасное взаимодействие с базами данных SQLite (navidrome.db и poisq.db).
//
// Что делает этот код и для чего он нужен:
// Данный файл repo_tracks_dedup_test.go тестирует метод интеллектуальной дедупликации DeduplicateTracks,
// проверяя, что при наличии повторяющихся записей (например, оригинал и «копия»)
// корректно сохраняется лучший трек, а дубликат удаляется.
package db

import (
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDeduplicateTracks(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "poisq_dedup_test.db")

	poisqDB, err := OpenPoisqDatabase(dbPath)
	if err != nil {
		t.Fatalf("ошибка открытия базы: %v", err)
	}
	defer poisqDB.Close()

	repo := NewTrackRepository(poisqDB)

	// Добавляем два трека Radiohead - Creep:
	// 1) Из основного каталога (без текста)
	// 2) Из каталога-копии
	// 3) Из каталога с синхронизированным текстом
	t1 := TrackRecord{
		ID:           "id_copy",
		FilePath:     "/music/Music — копия/Radiohead/Pablo Honey/01 Creep.flac",
		FolderPath:   "/music/Music — копия/Radiohead/Pablo Honey",
		Artist:       "Radiohead",
		Album:        "Pablo Honey",
		Title:        "Creep",
		Duration:     238,
		LyricsStatus: "none",
		HasCover:     false,
	}
	t2 := TrackRecord{
		ID:           "id_original",
		FilePath:     "/music/Music/Radiohead/Pablo Honey/01 Creep.flac",
		FolderPath:   "/music/Music/Radiohead/Pablo Honey",
		Artist:       "Radiohead",
		Album:        "Pablo Honey",
		Title:        "Creep",
		Duration:     238,
		LyricsStatus: "synced",
		HasCover:     true,
		CoverWidth:   600,
	}
	t3 := TrackRecord{
		ID:           "id_nirvana",
		FilePath:     "/music/Music/Nirvana/In Utero/01 Serve the Servants.flac",
		FolderPath:   "/music/Music/Nirvana/In Utero",
		Artist:       "Nirvana",
		Album:        "In Utero",
		Title:        "Serve the Servants",
		Duration:     216,
		LyricsStatus: "none",
	}

	_ = repo.UpsertTrack(t1)
	_ = repo.UpsertTrack(t2)
	_ = repo.UpsertTrack(t3)

	tracksBefore, _ := repo.ListTracks("all", "", 10)
	if len(tracksBefore) != 3 {
		t.Fatalf("ожидалось 3 трека до дедупликации, получено: %d", len(tracksBefore))
	}

	deleted, err := repo.DeduplicateTracks()
	if err != nil {
		t.Fatalf("ошибка дедупликации: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("ожидалось удаление 1 дубликата, удалено: %d", deleted)
	}

	tracksAfter, _ := repo.ListTracks("all", "", 10)
	if len(tracksAfter) != 2 {
		t.Fatalf("ожидалось 2 уникальных трека после дедупликации, получено: %d", len(tracksAfter))
	}

	// Проверяем, что оставшийся Creep — это качественный оригинал с текстом
	foundOriginal := false
	for _, tr := range tracksAfter {
		if tr.Title == "Creep" {
			if tr.ID != "id_original" {
				t.Errorf("остался не тот трек: %s (filePath: %s)", tr.ID, tr.FilePath)
			} else {
				foundOriginal = true
			}
		}
	}
	if !foundOriginal {
		t.Errorf("оригинальный трек Creep не найден среди оставшихся")
	}

	// Треки с пустым названием или с разной длительностью (>10 сек) не должны склеиваться
	_ = repo.UpsertTrack(TrackRecord{ID: "empty_1", FilePath: "/music/a1.flac", Artist: "A", Title: "", Duration: 180})
	_ = repo.UpsertTrack(TrackRecord{ID: "empty_2", FilePath: "/music/a2.flac", Artist: "A", Title: "", Duration: 180})
	_ = repo.UpsertTrack(TrackRecord{ID: "dur_short", FilePath: "/music/s1.flac", Artist: "B", Album: "Alb", Title: "Intro", Duration: 60})
	_ = repo.UpsertTrack(TrackRecord{ID: "dur_long", FilePath: "/music/s2.flac", Artist: "B", Album: "Alb", Title: "Intro", Duration: 220})

	deleted2, err := repo.DeduplicateTracks()
	if err != nil {
		t.Fatalf("ошибка повторной дедупликации: %v", err)
	}
	if deleted2 != 0 {
		t.Fatalf("треки без названия или с разной длительностью не должны удаляться, удалено: %d", deleted2)
	}
}

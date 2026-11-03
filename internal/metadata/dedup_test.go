// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл dedup_test.go проверяет дедупликацию треков из каталогов-копий («Music — копия»),
// гарантируя, что при наличии трека в основном каталоге дубликат отсеивается,
// а уникальный трек из папки-копии успешно сохраняется в библиотеку.
package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/F5GO/poisq/internal/db"
	_ "modernc.org/sqlite"
)

func TestSyncFromNavidromeDedup(t *testing.T) {
	tmpDir := t.TempDir()
	navPath := filepath.Join(tmpDir, "nav.db")
	poisqPath := filepath.Join(tmpDir, "poisq.db")

	navDB, err := sql.Open("sqlite", navPath)
	if err != nil {
		t.Fatalf("ошибка открытия nav.db: %v", err)
	}
	defer navDB.Close()

	_, err = navDB.Exec(`
		CREATE TABLE media_file (
			id TEXT PRIMARY KEY,
			path TEXT,
			title TEXT,
			artist TEXT,
			album TEXT,
			album_artist TEXT,
			track_number INTEGER,
			year INTEGER,
			duration REAL,
			has_cover_art INTEGER,
			embed_art_path TEXT
		);
		-- Основной трек
		INSERT INTO media_file VALUES ('t1', 'Music/Radiohead/Pablo Honey/01 Creep.flac', 'Creep', 'Radiohead', 'Pablo Honey', 'Radiohead', 1, 1993, 238.5, 1, '');
		-- Дубликат из каталога-копии (должен быть отсеян!)
		INSERT INTO media_file VALUES ('t2', 'Music — копия/Radiohead/Pablo Honey/01 Creep.flac', 'Creep', 'Radiohead', 'Pablo Honey', 'Radiohead', 1, 1993, 238.0, 1, '');
		-- Уникальный трек, который есть ТОЛЬКО в каталоге-копии (должен сохраниться!)
		INSERT INTO media_file VALUES ('t3', 'Music — копия/Nirvana/In Utero/01 Serve the Servants.flac', 'Serve the Servants', 'Nirvana', 'In Utero', 'Nirvana', 1, 1993, 216.0, 1, '');
	`)
	if err != nil {
		t.Fatalf("ошибка создания схемы nav.db: %v", err)
	}

	poisqDB, err := db.OpenPoisqDatabase(poisqPath)
	if err != nil {
		t.Fatalf("ошибка открытия poisq.db: %v", err)
	}
	defer poisqDB.Close()

	trackRepo := db.NewTrackRepository(poisqDB)
	stagingRepo := db.NewStagingRepository(poisqDB)
	scanner := NewLibraryScanner(tmpDir, trackRepo, stagingRepo, nil, nil, nil)

	synced, err := scanner.SyncFromNavidromeDB(navDB)
	if err != nil {
		t.Fatalf("ошибка синхронизации: %v", err)
	}

	if synced != 2 {
		t.Fatalf("ожидалось 2 уникальных трека, получено: %d", synced)
	}

	tracks, err := trackRepo.ListTracks("all", "", 100)
	if err != nil {
		t.Fatalf("ошибка получения треков: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("ожидалось 2 трека в базе, получено %d", len(tracks))
	}

	// Проверяем, что Creep остался из оригинального каталога
	foundCreep := false
	foundNirvana := false
	for _, tr := range tracks {
		if tr.Title == "Creep" {
			foundCreep = true
			if IsCopyPath(tr.FilePath) {
				t.Errorf("Creep не должен быть взят из каталога-копии: %s", tr.FilePath)
			}
		}
		if tr.Title == "Serve the Servants" {
			foundNirvana = true
		}
	}
	if !foundCreep || !foundNirvana {
		t.Errorf("не найдены ожидаемые треки (creep: %v, nirvana: %v)", foundCreep, foundNirvana)
	}
}

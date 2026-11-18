// Package db отвечает за безопасное взаимодействие с базой данных SQLite Navidrome.
//
// Данный файл db_test.go содержит тесты загрузки словаря, схемы отслеживания изменений
// и разовой очистки прежнего обогащения full_text в таблицах artist, album, media_file.
package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func createTestDB(t *testing.T) (*sql.DB, string) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_navidrome.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Ошибка создания тестовой БД: %v", err)
	}

	schema := `
	CREATE TABLE artist (
		id TEXT PRIMARY KEY,
		name TEXT,
		full_text TEXT,
		play_count INTEGER DEFAULT 0
	);
	CREATE TABLE album (
		id TEXT PRIMARY KEY,
		name TEXT,
		artist TEXT,
		full_text TEXT,
		play_count INTEGER DEFAULT 0
	);
	CREATE TABLE media_file (
		id TEXT PRIMARY KEY,
		title TEXT,
		artist TEXT,
		album TEXT,
		full_text TEXT,
		play_count INTEGER DEFAULT 0
	);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("Ошибка создания схемы: %v", err)
	}

	return db, dbPath
}

func TestEnricherAndLoader(t *testing.T) {
	db, _ := createTestDB(t)
	defer db.Close()

	// Вставляем тестовые данные
	_, err := db.Exec(`
		INSERT INTO artist (id, name, full_text, play_count) VALUES ('art-1', 'The Offspring', 'the offspring punk rock', 50);
		INSERT INTO artist (id, name, full_text, play_count) VALUES ('art-2', 'Кино', 'кино русский рок', 100);
		INSERT INTO album (id, name, artist, full_text, play_count) VALUES ('alb-1', 'Smash', 'The Offspring', 'smash offspring', 30);
		INSERT INTO media_file (id, title, artist, album, full_text, play_count) VALUES ('tr-1', 'Self Esteem', 'The Offspring', 'Smash', 'self esteem', 20);
	`)
	if err != nil {
		t.Fatalf("Ошибка вставки данных: %v", err)
	}

	// 1. Тестируем Loader
	items, err := LoadLibraryVocabulary(db)
	if err != nil {
		t.Fatalf("Ошибка LoadLibraryVocabulary: %v", err)
	}
	if len(items) != 4 {
		t.Errorf("Ожидалось 4 элемента в словаре, получено %d", len(items))
	}

}

func TestRemoveLegacyEnrichment(t *testing.T) {
	db, _ := createTestDB(t)
	defer db.Close()
	_, err := db.Exec(`
		CREATE TABLE _poisq_enrichment (entity_table TEXT, entity_id TEXT, original_hash TEXT);
		INSERT INTO artist (id, name, full_text) VALUES ('a1', 'Кино', 'кино русский рок [poisq: kino]');
		INSERT INTO artist (id, name, full_text) VALUES ('a2', 'X', '[poisq: iks]');
		INSERT INTO album (id, name, full_text) VALUES ('b1', 'Smash', 'smash offspring [ndss: smesh]');
		INSERT INTO media_file (id, title, full_text) VALUES ('m1', 'Self Esteem', 'self esteem');
	`)
	if err != nil {
		t.Fatal(err)
	}

	n, err := RemoveLegacyEnrichment(db)
	if err != nil || n != 3 {
		t.Fatalf("очищено %d записей, ошибка %v; ждали 3", n, err)
	}
	for id, want := range map[string]string{"a1": "кино русский рок", "a2": ""} {
		var got string
		_ = db.QueryRow(`SELECT full_text FROM artist WHERE id = ?`, id).Scan(&got)
		if got != want {
			t.Errorf("artist %s: full_text=%q, ждали %q", id, got, want)
		}
	}
	var album, track string
	_ = db.QueryRow(`SELECT full_text FROM album WHERE id = 'b1'`).Scan(&album)
	_ = db.QueryRow(`SELECT full_text FROM media_file WHERE id = 'm1'`).Scan(&track)
	if album != "smash offspring" || track != "self esteem" {
		t.Errorf("album=%q track=%q", album, track)
	}

	// таблица удалена: повторный запуск ничего не трогает
	if _, err := db.Exec(`UPDATE artist SET full_text = 'ручной [poisq: x]' WHERE id = 'a1'`); err != nil {
		t.Fatal(err)
	}
	if n, err := RemoveLegacyEnrichment(db); err != nil || n != 0 {
		t.Fatalf("повторный запуск: %d, %v", n, err)
	}
}

func TestSQLInjectionPrevention(t *testing.T) {
	// Проверка валидации таблиц
	if err := ValidateIdentifier("artist", true); err != nil {
		t.Errorf("Ожидалось успешное прохождение для 'artist': %v", err)
	}
	if err := ValidateIdentifier("artist; DROP TABLE user;--", true); err == nil {
		t.Errorf("Ожидалась ошибка валидации SQL injection в имени таблицы")
	}

	// Проверка валидации столбцов
	if err := ValidateIdentifier("name", false); err != nil {
		t.Errorf("Ожидалось успешное прохождение для 'name': %v", err)
	}
	if err := ValidateIdentifier("name; SELECT password FROM user;--", false); err == nil {
		t.Errorf("Ожидалась ошибка валидации SQL injection в имени столбца")
	}
}

func TestResetDatabaseClearsArtistBiographies(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatalf("open poisq database: %v", err)
	}
	defer database.Close()

	if _, err := database.Exec(`
		INSERT INTO api_keys(provider, label, key_encrypted, key_mask) VALUES ('gemini', 'keep', 'encrypted', '***');
		INSERT INTO artist_bio_identity(artist_id, status) VALUES ('artist', 'resolved');
		INSERT INTO artist_bio_candidates(
			artist_id, provider, source_artist_id, original_text, status
		) VALUES ('artist', 'deezer', '42', 'Описание', 'accepted');
		INSERT INTO artist_biographies(artist_id, automatic_candidate_id, manual_text)
		SELECT 'artist', id, 'Ручной вариант' FROM artist_bio_candidates WHERE artist_id = 'artist';
	`); err != nil {
		t.Fatalf("seed biography state: %v", err)
	}

	if err := ResetDatabase(database, true); err != nil {
		t.Fatalf("reset database: %v", err)
	}
	for _, table := range []string{"artist_biographies", "artist_bio_candidates", "artist_bio_identity"} {
		var count int
		if err := database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("table %s was not cleared: %d", table, count)
		}
	}
	var keyCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE label = 'keep'`).Scan(&keyCount); err != nil {
		t.Fatalf("count preserved API keys: %v", err)
	}
	if keyCount != 1 {
		t.Fatalf("API keys were not preserved: %d", keyCount)
	}
}

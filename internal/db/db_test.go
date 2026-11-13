// Package db отвечает за безопасное взаимодействие с базой данных SQLite Navidrome.
//
// Данный файл db_test.go содержит тесты загрузки словаря, схемы отслеживания изменений
// и безопасного обогащения столбца full_text в таблицах artist, album, media_file.
package db

import (
	"database/sql"
	"path/filepath"
	"strings"
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

	// 2. Тестируем Enricher
	enricher := NewEnricher(db, true)
	if err := enricher.InitSchema(); err != nil {
		t.Fatalf("Ошибка InitSchema: %v", err)
	}

	enricher.RunEnrichment()

	// Проверяем, что в artist full_text добавились транслитерации
	var ftOffspring string
	err = db.QueryRow("SELECT full_text FROM artist WHERE id = 'art-1'").Scan(&ftOffspring)
	if err != nil {
		t.Fatalf("Ошибка чтения full_text Offspring: %v", err)
	}
	if !strings.Contains(ftOffspring, "[poisq:") || !strings.Contains(ftOffspring, "офспринг") {
		t.Errorf("full_text Offspring не содержит транслитерации: %s", ftOffspring)
	}

	var ftKino string
	err = db.QueryRow("SELECT full_text FROM artist WHERE id = 'art-2'").Scan(&ftKino)
	if err != nil {
		t.Fatalf("Ошибка чтения full_text Кино: %v", err)
	}
	if !strings.Contains(ftKino, "[poisq:") || !strings.Contains(ftKino, "kino") {
		t.Errorf("full_text Кино не содержит транслитерации 'kino': %s", ftKino)
	}

	// 3. Тестируем идемпотентность (повторный прогон не должен ничего ломать или дублировать)
	enricher.RunEnrichment()

	var ftKinoSecond string
	_ = db.QueryRow("SELECT full_text FROM artist WHERE id = 'art-2'").Scan(&ftKinoSecond)
	if strings.Count(ftKinoSecond, "[poisq:") != 1 {
		t.Errorf("Маркер [poisq: сдублировался при повторном запуске: %s", ftKinoSecond)
	}

	// 4. Navidrome пересобрал full_text (файл изменился, название то же) — вставка должна вернуться
	if _, err := db.Exec("UPDATE artist SET full_text = 'кино русский рок' WHERE id = 'art-2'"); err != nil {
		t.Fatal(err)
	}
	enricher.RunEnrichment()
	var ftKinoRewritten string
	_ = db.QueryRow("SELECT full_text FROM artist WHERE id = 'art-2'").Scan(&ftKinoRewritten)
	if !strings.Contains(ftKinoRewritten, "[poisq:") {
		t.Errorf("после пересборки full_text вставка poisq не вернулась: %s", ftKinoRewritten)
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

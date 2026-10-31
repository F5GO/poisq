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

// Package db отвечает за безопасное взаимодействие с базами данных SQLite (navidrome.db и poisq.db).
//
// Данный файл poisq_db.go инициализирует собственную базу данных микросервиса (poisq.db)
// в режиме WAL, автоматически создает родительский каталог и выполняет миграции схемы
// для 5 ключевых таблиц: api_keys, tracks, staging_changes, lyrics_jobs и provider_cache.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// OpenPoisqDatabase открывает или создает файл базы данных poisq.db и применяет миграции схемы.
func OpenPoisqDatabase(dbPath string) (*sql.DB, error) {
	dir := filepath.Dir(dbPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("не удалось создать директорию для poisq.db (%s): %w", dir, err)
		}
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)
	sqliteDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия poisq.db: %w", err)
	}

	sqliteDB.SetMaxOpenConns(4)
	sqliteDB.SetMaxIdleConns(2)
	sqliteDB.SetConnMaxLifetime(1 * time.Hour)

	if err := sqliteDB.Ping(); err != nil {
		_ = sqliteDB.Close()
		return nil, fmt.Errorf("ошибка ping poisq.db: %w", err)
	}

	if err := initPoisqSchema(sqliteDB); err != nil {
		_ = sqliteDB.Close()
		return nil, fmt.Errorf("ошибка миграции схемы poisq.db: %w", err)
	}

	return sqliteDB, nil
}

func initPoisqSchema(sqliteDB *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS api_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		provider TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		key_encrypted TEXT NOT NULL,
		key_mask TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		cooldown_until INTEGER NOT NULL DEFAULT 0,
		last_used_at INTEGER NOT NULL DEFAULT 0,
		success_count INTEGER NOT NULL DEFAULT 0,
		error_count INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_api_keys_provider_status ON api_keys(provider, status, last_used_at);

	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS tracks (
		id TEXT PRIMARY KEY,
		file_path TEXT NOT NULL,
		folder_path TEXT NOT NULL DEFAULT '',
		artist TEXT NOT NULL DEFAULT '',
		album TEXT NOT NULL DEFAULT '',
		album_artist TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		track_number INTEGER NOT NULL DEFAULT 0,
		year INTEGER NOT NULL DEFAULT 0,
		duration INTEGER NOT NULL DEFAULT 0,
		lang_code TEXT NOT NULL DEFAULT '',
		has_cover INTEGER NOT NULL DEFAULT 0,
		cover_width INTEGER NOT NULL DEFAULT 0,
		cover_height INTEGER NOT NULL DEFAULT 0,
		is_mojibake INTEGER NOT NULL DEFAULT 0,
		lyrics_status TEXT NOT NULL DEFAULT 'none',
		active_source TEXT NOT NULL DEFAULT '',
		active_lyrics TEXT NOT NULL DEFAULT '',
		lrc_file_synced INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_tracks_folder ON tracks(folder_path);
	CREATE INDEX IF NOT EXISTS idx_tracks_lyrics_status ON tracks(lyrics_status);
	CREATE INDEX IF NOT EXISTS idx_tracks_mojibake ON tracks(is_mojibake);

	CREATE TABLE IF NOT EXISTS staging_changes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		scope TEXT NOT NULL DEFAULT 'track',
		target_id TEXT NOT NULL,
		folder_path TEXT NOT NULL DEFAULT '',
		change_type TEXT NOT NULL,
		confidence INTEGER NOT NULL DEFAULT 0,
		current_json TEXT NOT NULL DEFAULT '{}',
		proposed_json TEXT NOT NULL DEFAULT '{}',
		original_tags_json TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'pending',
		created_at INTEGER NOT NULL DEFAULT 0,
		applied_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_staging_status ON staging_changes(status, change_type);

	CREATE TABLE IF NOT EXISTS lyrics_jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		track_id TEXT NOT NULL UNIQUE,
		job_type TEXT NOT NULL DEFAULT 'full_pipeline',
		priority INTEGER NOT NULL DEFAULT 1,
		status TEXT NOT NULL DEFAULT 'pending',
		attempt_count INTEGER NOT NULL DEFAULT 0,
		next_retry_at INTEGER NOT NULL DEFAULT 0,
		error_log TEXT NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_lyrics_jobs_queue ON lyrics_jobs(status, next_retry_at, priority DESC);

	CREATE TABLE IF NOT EXISTS provider_cache (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		track_id TEXT NOT NULL,
		provider TEXT NOT NULL,
		kind TEXT NOT NULL DEFAULT 'plain',
		score REAL NOT NULL DEFAULT 0,
		raw_lyrics TEXT NOT NULL DEFAULT '',
		metadata_json TEXT NOT NULL DEFAULT '{}',
		fetched_at INTEGER NOT NULL DEFAULT 0,
		UNIQUE(track_id, provider)
	);
	CREATE INDEX IF NOT EXISTS idx_provider_cache_track ON provider_cache(track_id);

	CREATE TABLE IF NOT EXISTS ai_alias_cache (
		cache_key TEXT PRIMARY KEY,
		value_json TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL DEFAULT 0
	);

	-- Уверенность акустического выравнивания по строкам (для подсветки сомнительных строк в редакторе)
	CREATE TABLE IF NOT EXISTS lrc_confidence (
		track_id TEXT PRIMARY KEY,
		lines_json TEXT NOT NULL DEFAULT '[]',
		summary_json TEXT NOT NULL DEFAULT '{}',
		updated_at INTEGER NOT NULL DEFAULT 0
	);

	-- Ручные правки таймингов в редакторе (было -> стало): настоящий эталон для проверки и настройки выравнивания
	CREATE TABLE IF NOT EXISTS lrc_corrections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		track_id TEXT NOT NULL,
		line_text TEXT NOT NULL,
		auto_sec REAL NOT NULL,
		corrected_sec REAL NOT NULL,
		confidence TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_lrc_corrections_track ON lrc_corrections(track_id);
	`
	_, err := sqliteDB.Exec(schema)
	return err
}

// ResetDatabase выполняет полный сброс базы данных poisq.db:
// очищает таблицы staging_changes, tracks, lyrics_jobs и provider_cache.
// Если keepAPIKeys == true, сохраняет настроенные ключи Gemini/Groq.
func ResetDatabase(db *sql.DB, keepAPIKeys bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tables := []string{"staging_changes", "tracks", "lyrics_jobs", "provider_cache", "lrc_confidence", "lrc_corrections"}
	if !keepAPIKeys {
		tables = append(tables, "api_keys")
	}

	for _, tbl := range tables {
		if _, err := tx.Exec("DELETE FROM " + tbl); err != nil {
			return fmt.Errorf("ошибка очистки таблицы %s: %w", tbl, err)
		}
	}

	// Сброс автоинкрементов в sqlite_sequence
	for _, tbl := range tables {
		_, _ = tx.Exec("DELETE FROM sqlite_sequence WHERE name = ?", tbl)
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Сжимаем страницы SQLite
	_, _ = db.Exec("VACUUM;")
	return nil
}

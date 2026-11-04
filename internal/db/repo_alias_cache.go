// Package db отвечает за безопасное взаимодействие с базами данных SQLite (navidrome.db и poisq.db).
//
// Данный файл repo_alias_cache.go хранит кэш ИИ-подсказок написания исполнителей и названий
// (таблица ai_alias_cache), чтобы нейросеть вызывалась один раз на трек и дальше поиск оставался мгновенным.
package db

import (
	"database/sql"
	"time"
)

// AliasCacheRepository — хранилище кэша подсказок LLM.
type AliasCacheRepository struct {
	db *sql.DB
}

// NewAliasCacheRepository создаёт репозиторий кэша подсказок.
func NewAliasCacheRepository(db *sql.DB) *AliasCacheRepository {
	return &AliasCacheRepository{db: db}
}

// Get возвращает сохранённое значение по ключу.
func (r *AliasCacheRepository) Get(key string) (string, bool) {
	var v string
	if err := r.db.QueryRow(`SELECT value_json FROM ai_alias_cache WHERE cache_key = ?`, key).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

// Set сохраняет или обновляет значение.
func (r *AliasCacheRepository) Set(key, valueJSON string) error {
	_, err := r.db.Exec(`
		INSERT INTO ai_alias_cache (cache_key, value_json, created_at) VALUES (?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET value_json = excluded.value_json, created_at = excluded.created_at`,
		key, valueJSON, time.Now().Unix())
	return err
}

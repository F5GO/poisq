package db

import (
	"database/sql"
	"errors"
)

// GetSetting возвращает значение настройки из таблицы settings; пустая строка — настройка не задана.
func GetSetting(db *sql.DB, key string) (string, error) {
	var v string
	err := db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting сохраняет значение настройки (вставка или замена).
func SetSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

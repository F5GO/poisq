// Package db отвечает за безопасное взаимодействие с базой данных SQLite Navidrome.
//
// Данный файл sqlite.go реализует инициализацию подключения к SQLite через чистый Go-драйвер
// modernc.org/sqlite с установкой режима WAL, валидацией идентификаторов по белому списку (SQL Injection Prevention)
// и таймаутами ожидания блокировок.
package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Белый список допустимых таблиц для исключения любых SQL-инъекций
var allowedTables = map[string]bool{
	"artist":            true,
	"album":             true,
	"media_file":        true,
	"_poisq_enrichment": true,
}

// Белый список допустимых столбцов для динамических запросов
var allowedColumns = map[string]bool{
	"id":            true,
	"name":          true,
	"title":         true,
	"full_text":     true,
	"play_count":    true,
	"entity_table":  true,
	"entity_id":     true,
	"original_hash": true,
	"enriched_at":   true,
}

// ValidateIdentifier проверяет имя таблицы или столбца по строгому белому списку разрешенных идентификаторов.
func ValidateIdentifier(identifier string, isTable bool) error {
	clean := strings.ToLower(strings.TrimSpace(identifier))
	if isTable {
		if !allowedTables[clean] {
			return fmt.Errorf("недопустимое имя таблицы для безопасного запроса: %q", identifier)
		}
	} else {
		if !allowedColumns[clean] {
			return fmt.Errorf("недопустимое имя столбца для безопасного запроса: %q", identifier)
		}
	}
	return nil
}

// OpenDatabase открывает базу данных Navidrome с безопасными параметрами WAL и таймаутов.
func OpenDatabase(dbPath string) (*sql.DB, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("файл базы данных не найден по пути %q: %w", dbPath, err)
	}

	// Строка подключения с обязательными прагмами для бесконфликтной работы с Navidrome
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(OFF)", dbPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия SQLite базы данных: %w", err)
	}

	// Пул соединений: небольшое количество для снижения вероятности SQLite busy-локов
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(1 * time.Hour)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("не удалось выполнить ping к SQLite: %w", err)
	}

	// Удостоверимся, что режим WAL активен
	var journalMode string
	err = db.QueryRow("PRAGMA journal_mode;").Scan(&journalMode)
	if err == nil {
		log.Printf("[INFO] Режим SQLite Journal: %s", strings.ToUpper(journalMode))
	}

	return db, nil
}

// GetTableColumns возвращает множество имен столбцов таблицы для адаптивности к разным версиям Navidrome.
func GetTableColumns(db *sql.DB, tableName string) (map[string]bool, error) {
	if err := ValidateIdentifier(tableName, true); err != nil {
		return nil, err
	}
	query := fmt.Sprintf("PRAGMA table_info(%s);", tableName)
	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения структуры таблицы %s: %w", tableName, err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		columns[strings.ToLower(name)] = true
	}

	return columns, rows.Err()
}

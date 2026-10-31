// Package db отвечает за безопасное взаимодействие с базой данных SQLite Navidrome.
//
// Данный файл enricher.go реализует фоновое безопасное обогащение столбца full_text
// в таблицах artist, album и media_file фонетическими транслитерациями (RU <-> EN)
// с защитой от зацикливания через хэш-таблицу отслеживания и короткими WAL-транзакциями.
package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/search"
)

var poisqMarkerRegex = regexp.MustCompile(`\s*\[(poisq|ndss):.*?\]`)

// Enricher управляет процессом обогащения full_text в SQLite.
type Enricher struct {
	db      *sql.DB
	enabled bool
}

// NewEnricher создает новый экземпляр Enricher.
func NewEnricher(db *sql.DB, enabled bool) *Enricher {
	return &Enricher{
		db:      db,
		enabled: enabled,
	}
}

// InitSchema создает служебную таблицу отслеживания изменений, если она еще не создана.
func (e *Enricher) InitSchema() error {
	query := `
	CREATE TABLE IF NOT EXISTS _poisq_enrichment (
		entity_table TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		original_hash TEXT NOT NULL,
		enriched_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (entity_table, entity_id)
	);`

	_, err := e.db.Exec(query)
	if err != nil {
		return fmt.Errorf("ошибка создания таблицы _poisq_enrichment: %w", err)
	}
	return nil
}

// RunEnrichment выполняет один полный цикл проверки и обогащения таблиц Navidrome.
func (e *Enricher) RunEnrichment() {
	if !e.enabled {
		log.Println("[INFO] Обогащение SQLite full_text отключено в конфигурации")
		return
	}

	start := time.Now()
	totalUpdated := 0

	// Обогащение artist
	if n, err := e.enrichTable("artist", "name"); err != nil {
		log.Printf("[WARN] Ошибка обогащения таблицы artist: %v", err)
	} else {
		totalUpdated += n
	}

	// Обогащение album
	albumCols, err := GetTableColumns(e.db, "album")
	if err == nil {
		nameCol := "name"
		if !albumCols["name"] && albumCols["title"] {
			nameCol = "title"
		}
		if n, err := e.enrichTable("album", nameCol); err != nil {
			log.Printf("[WARN] Ошибка обогащения таблицы album: %v", err)
		} else {
			totalUpdated += n
		}
	}

	// Обогащение media_file
	if n, err := e.enrichTable("media_file", "title"); err != nil {
		log.Printf("[WARN] Ошибка обогащения таблицы media_file: %v", err)
	} else {
		totalUpdated += n
	}

	log.Printf("[INFO] Цикл обогащения full_text завершен за %v. Обновлено записей: %d", time.Since(start).Round(time.Millisecond), totalUpdated)
}

type entityRow struct {
	id       string
	source   string
	fullText string
	hash     string
}

func (e *Enricher) enrichTable(tableName, sourceCol string) (int, error) {
	if err := ValidateIdentifier(tableName, true); err != nil {
		return 0, err
	}
	if err := ValidateIdentifier(sourceCol, false); err != nil {
		return 0, err
	}

	cols, err := GetTableColumns(e.db, tableName)
	if err != nil {
		return 0, err
	}

	if !cols["full_text"] || !cols["id"] || !cols[sourceCol] {
		// Таблица не имеет нужных столбцов, пропускаем
		return 0, nil
	}

	// Выбираем только те записи, которые еще не обогащены или у которых изменился текст
	query := fmt.Sprintf(`
		SELECT t.id, t.%s, COALESCE(t.full_text, ''), COALESCE(e.original_hash, '')
		FROM %s t
		LEFT JOIN _poisq_enrichment e ON e.entity_table = '%s' AND e.entity_id = t.id
		WHERE t.%s IS NOT NULL AND t.%s != ''
	`, sourceCol, tableName, tableName, sourceCol, sourceCol)

	rows, err := e.db.Query(query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var toUpdate []entityRow

	for rows.Next() {
		var id, source, fullText, currentHash string
		if err := rows.Scan(&id, &source, &fullText, &currentHash); err != nil {
			continue
		}

		hash := computeHash(source)
		if hash != currentHash {
			toUpdate = append(toUpdate, entityRow{
				id:       id,
				source:   source,
				fullText: fullText,
				hash:     hash,
			})
		}
	}

	if len(toUpdate) == 0 {
		return 0, nil
	}

	// Применяем изменения короткими пакетами (по 50 строк) для минимизации блокировок SQLite WAL
	const batchSize = 50
	totalCount := 0

	for i := 0; i < len(toUpdate); i += batchSize {
		end := i + batchSize
		if end > len(toUpdate) {
			end = len(toUpdate)
		}

		batch := toUpdate[i:end]
		if err := e.updateBatch(tableName, batch); err != nil {
			log.Printf("[ERROR] Ошибка применения пакета для %s: %v", tableName, err)
			return totalCount, err
		}
		totalCount += len(batch)
	}

	return totalCount, nil
}

func (e *Enricher) updateBatch(tableName string, batch []entityRow) error {
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	updateFullTextStmt, err := tx.Prepare(fmt.Sprintf("UPDATE %s SET full_text = ? WHERE id = ?", tableName))
	if err != nil {
		return err
	}
	defer updateFullTextStmt.Close()

	upsertMetaStmt, err := tx.Prepare(`
		INSERT INTO _poisq_enrichment (entity_table, entity_id, original_hash, enriched_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(entity_table, entity_id) DO UPDATE SET
			original_hash = excluded.original_hash,
			enriched_at = CURRENT_TIMESTAMP
	`)
	if err != nil {
		return err
	}
	defer upsertMetaStmt.Close()

	for _, item := range batch {
		tokens := generateEnrichmentTokens(item.source)
		cleanBase := poisqMarkerRegex.ReplaceAllString(item.fullText, "")
		cleanBase = strings.TrimSpace(cleanBase)

		newFullText := cleanBase
		if tokens != "" {
			if newFullText != "" {
				newFullText = newFullText + " [poisq: " + tokens + "]"
			} else {
				newFullText = "[poisq: " + tokens + "]"
			}
		}

		if _, err := updateFullTextStmt.Exec(newFullText, item.id); err != nil {
			return err
		}

		if _, err := upsertMetaStmt.Exec(tableName, item.id, item.hash); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func computeHash(text string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(h[:])
}

// generateEnrichmentTokens формирует строку нормализованных транслитераций для full_text.
func generateEnrichmentTokens(text string) string {
	variants := search.GenerateVariants(text)
	var tokens []string
	seen := make(map[string]bool)

	lowerOriginal := strings.ToLower(strings.TrimSpace(text))
	seen[lowerOriginal] = true

	for _, v := range variants {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" && !seen[v] {
			seen[v] = true
			tokens = append(tokens, v)
		}
	}

	return strings.Join(tokens, " ")
}

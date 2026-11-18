package db

import (
	"database/sql"
	"fmt"
)

// RemoveLegacyEnrichment убирает следы прежнего обогащения full_text в navidrome.db: хвосты «[poisq: …]» / «[ndss: …]»
// и таблицу _poisq_enrichment. Это последняя запись poisq в базу Navidrome: после удаления таблицы функция ничего не делает,
// а база Navidrome только читается.
func RemoveLegacyEnrichment(navDB *sql.DB) (int64, error) {
	var tables int
	if err := navDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = '_poisq_enrichment'`).Scan(&tables); err != nil || tables == 0 {
		return 0, err
	}

	var withFullText []string
	for _, table := range []string{"artist", "album", "media_file"} {
		cols, err := GetTableColumns(navDB, table)
		if err != nil {
			return 0, err
		}
		if cols["full_text"] {
			withFullText = append(withFullText, table)
		}
	}

	tx, err := navDB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var cleaned int64
	for _, table := range withFullText {
		// Маркер всегда дописывался в конец full_text, поэтому отрезаем всё начиная с него.
		for _, marker := range []string{"[poisq:", "[ndss:"} {
			res, err := tx.Exec(`UPDATE `+table+` SET full_text = rtrim(substr(full_text, 1, instr(full_text, ?) - 1)) WHERE instr(full_text, ?) > 0`, marker, marker)
			if err != nil {
				return 0, fmt.Errorf("очистка full_text в %s: %w", table, err)
			}
			n, _ := res.RowsAffected()
			cleaned += n
		}
	}
	if _, err := tx.Exec(`DROP TABLE _poisq_enrichment`); err != nil {
		return 0, err
	}
	return cleaned, tx.Commit()
}

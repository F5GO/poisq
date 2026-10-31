// Package db отвечает за безопасное взаимодействие с базой данных SQLite Navidrome.
//
// Данный файл loader.go реализует чтение артистов, альбомов и медиафайлов (треков)
// из базы данных Navidrome для последующей загрузки в оперативный словарь интеллектуального поиска.
package db

import (
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/F5GO/poisq/internal/search"
)

// LoadLibraryVocabulary вычитывает всех артистов, альбомы и треки из Navidrome.
func LoadLibraryVocabulary(db *sql.DB) ([]search.DictItem, error) {
	var items []search.DictItem

	// 1. Загрузка артистов
	artistItems, err := loadArtists(db)
	if err != nil {
		log.Printf("[WARN] Не удалось загрузить исполнителей: %v", err)
	} else {
		items = append(items, artistItems...)
	}

	// 2. Загрузка альбомов
	albumItems, err := loadAlbums(db)
	if err != nil {
		log.Printf("[WARN] Не удалось загрузить альбомы: %v", err)
	} else {
		items = append(items, albumItems...)
	}

	// 3. Загрузка треков (media_file)
	trackItems, err := loadTracks(db)
	if err != nil {
		log.Printf("[WARN] Не удалось загрузить треки: %v", err)
	} else {
		items = append(items, trackItems...)
	}

	log.Printf("[INFO] Загружено %d элементов библиотеки для словаря поиска", len(items))
	return items, nil
}

func loadArtists(db *sql.DB) ([]search.DictItem, error) {
	cols, err := GetTableColumns(db, "artist")
	if err != nil || !cols["name"] {
		return nil, fmt.Errorf("таблица artist недоступна или нет колонки name: %w", err)
	}

	hasPlayCount := cols["play_count"]
	query := "SELECT name, 0 FROM artist WHERE name IS NOT NULL AND name != ''"
	if hasPlayCount {
		query = "SELECT name, COALESCE(play_count, 0) FROM artist WHERE name IS NOT NULL AND name != ''"
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []search.DictItem
	for rows.Next() {
		var name string
		var playCount int
		if err := rows.Scan(&name, &playCount); err != nil {
			continue
		}
		clean := strings.TrimSpace(name)
		if clean != "" {
			result = append(result, search.DictItem{
				Text:       clean,
				EntityType: "artist",
				PlayCount:  playCount,
			})
		}
	}

	return result, rows.Err()
}

func loadAlbums(db *sql.DB) ([]search.DictItem, error) {
	cols, err := GetTableColumns(db, "album")
	if err != nil {
		return nil, err
	}

	nameCol := "name"
	if !cols["name"] && cols["title"] {
		nameCol = "title"
	}

	if !cols[nameCol] {
		return nil, fmt.Errorf("в таблице album не найдена колонка name/title")
	}

	if err := ValidateIdentifier(nameCol, false); err != nil {
		return nil, err
	}

	hasPlayCount := cols["play_count"]
	query := fmt.Sprintf("SELECT %s, 0 FROM album WHERE %s IS NOT NULL AND %s != ''", nameCol, nameCol, nameCol)
	if hasPlayCount {
		query = fmt.Sprintf("SELECT %s, COALESCE(play_count, 0) FROM album WHERE %s IS NOT NULL AND %s != ''", nameCol, nameCol, nameCol)
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []search.DictItem
	for rows.Next() {
		var title string
		var playCount int
		if err := rows.Scan(&title, &playCount); err != nil {
			continue
		}
		clean := strings.TrimSpace(title)
		if clean != "" {
			result = append(result, search.DictItem{
				Text:       clean,
				EntityType: "album",
				PlayCount:  playCount,
			})
		}
	}

	return result, rows.Err()
}

func loadTracks(db *sql.DB) ([]search.DictItem, error) {
	cols, err := GetTableColumns(db, "media_file")
	if err != nil || !cols["title"] {
		return nil, fmt.Errorf("таблица media_file недоступна или нет колонки title: %w", err)
	}

	hasPlayCount := cols["play_count"]
	query := "SELECT title, 0 FROM media_file WHERE title IS NOT NULL AND title != ''"
	if hasPlayCount {
		query = "SELECT title, COALESCE(play_count, 0) FROM media_file WHERE title IS NOT NULL AND title != ''"
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []search.DictItem
	for rows.Next() {
		var title string
		var playCount int
		if err := rows.Scan(&title, &playCount); err != nil {
			continue
		}
		clean := strings.TrimSpace(title)
		if clean != "" {
			result = append(result, search.DictItem{
				Text:       clean,
				EntityType: "track",
				PlayCount:  playCount,
			})
		}
	}

	return result, rows.Err()
}

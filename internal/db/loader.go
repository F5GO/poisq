// Package db отвечает за безопасное взаимодействие с базой данных SQLite Navidrome.
//
// Данный файл loader.go реализует чтение артистов, альбомов и медиафайлов (треков)
// из базы данных Navidrome для последующей загрузки в оперативный словарь интеллектуального поиска.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/F5GO/poisq/internal/search"
)

const MaxArtistInventoryPage = 200
const MaxArtistSearchQueryBytes = 100

type LibraryArtist struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LoadArtistInventoryPage returns a deterministic bounded page from Navidrome's artist inventory.
func LoadArtistInventoryPage(ctx context.Context, database *sql.DB, afterID string, limit int) ([]LibraryArtist, error) {
	if database == nil {
		return nil, fmt.Errorf("Navidrome database is required")
	}
	if len(afterID) > 512 {
		return nil, fmt.Errorf("artist cursor exceeds limit")
	}
	limit = max(1, min(limit, MaxArtistInventoryPage))
	cols, err := GetTableColumns(database, "artist")
	if err != nil || !cols["id"] || !cols["name"] {
		return nil, fmt.Errorf("artist inventory requires id and name columns")
	}
	rows, err := database.QueryContext(ctx, `
		SELECT id, name FROM artist
		WHERE id > ? AND name IS NOT NULL AND TRIM(name) != ''
		ORDER BY id
		LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("read artist inventory: %w", err)
	}
	defer rows.Close()
	return scanLibraryArtists(rows, limit)
}

// SearchArtistInventory performs a bounded literal substring search over the
// real Navidrome artist inventory. SQL LIKE metacharacters are escaped.
func SearchArtistInventory(ctx context.Context, database *sql.DB, query string, limit int) ([]LibraryArtist, error) {
	if database == nil {
		return nil, fmt.Errorf("Navidrome database is required")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return LoadArtistInventoryPage(ctx, database, "", limit)
	}
	if !utf8.ValidString(query) || len(query) > MaxArtistSearchQueryBytes || containsControl(query) {
		return nil, fmt.Errorf("invalid artist search query")
	}
	limit = max(1, min(limit, 50))
	if err := requireArtistInventorySchema(database); err != nil {
		return nil, err
	}
	patterns := []string{
		"%" + escapeLikeLiteral(query) + "%",
		"%" + escapeLikeLiteral(strings.ToLower(query)) + "%",
		"%" + escapeLikeLiteral(strings.ToUpper(query)) + "%",
		"%" + escapeLikeLiteral(upperFirstRune(strings.ToLower(query))) + "%",
	}
	rows, err := database.QueryContext(ctx, `
		SELECT id, name FROM artist
		WHERE name IS NOT NULL AND TRIM(name) != ''
			AND (name LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\')
		ORDER BY LOWER(TRIM(name)), id
		LIMIT ?`, patterns[0], patterns[1], patterns[2], patterns[3], limit)
	if err != nil {
		return nil, fmt.Errorf("search artist inventory: %w", err)
	}
	defer rows.Close()
	return scanLibraryArtists(rows, limit)
}

// FindLibraryArtist confirms one bounded artist ID in Navidrome.
func FindLibraryArtist(ctx context.Context, database *sql.DB, artistID string) (LibraryArtist, bool, error) {
	if database == nil {
		return LibraryArtist{}, false, fmt.Errorf("Navidrome database is required")
	}
	if !validLibraryArtistID(artistID) {
		return LibraryArtist{}, false, fmt.Errorf("invalid artist id")
	}
	if err := requireArtistInventorySchema(database); err != nil {
		return LibraryArtist{}, false, err
	}
	var artist LibraryArtist
	err := database.QueryRowContext(ctx, `SELECT id, name FROM artist WHERE id = ? AND name IS NOT NULL AND TRIM(name) != '' LIMIT 1`, artistID).Scan(&artist.ID, &artist.Name)
	if err == sql.ErrNoRows {
		return LibraryArtist{}, false, nil
	}
	if err != nil {
		return LibraryArtist{}, false, fmt.Errorf("read artist inventory item: %w", err)
	}
	artist.ID = strings.TrimSpace(artist.ID)
	artist.Name = strings.TrimSpace(artist.Name)
	if !validLibraryArtistID(artist.ID) || artist.Name == "" || len(artist.Name) > 2<<10 || !utf8.ValidString(artist.Name) {
		return LibraryArtist{}, false, nil
	}
	return artist, true, nil
}

func requireArtistInventorySchema(database *sql.DB) error {
	cols, err := GetTableColumns(database, "artist")
	if err != nil || !cols["id"] || !cols["name"] {
		return fmt.Errorf("artist inventory requires id and name columns")
	}
	return nil
}

func scanLibraryArtists(rows *sql.Rows, capacity int) ([]LibraryArtist, error) {
	result := make([]LibraryArtist, 0, capacity)
	for rows.Next() {
		var artist LibraryArtist
		if err := rows.Scan(&artist.ID, &artist.Name); err != nil {
			return nil, fmt.Errorf("scan artist inventory: %w", err)
		}
		artist.ID = strings.TrimSpace(artist.ID)
		artist.Name = strings.TrimSpace(artist.Name)
		if !validLibraryArtistID(artist.ID) || artist.Name == "" || len(artist.Name) > 2<<10 || !utf8.ValidString(artist.Name) {
			continue
		}
		result = append(result, artist)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artist inventory: %w", err)
	}
	return result, nil
}

func escapeLikeLiteral(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func validLibraryArtistID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 512 && utf8.ValidString(value) && !containsControl(value)
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func upperFirstRune(value string) string {
	for index, r := range value {
		return string(unicode.ToUpper(r)) + value[index+utf8.RuneLen(r):]
	}
	return value
}

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

// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл cover_locator_test.go тестирует работу универсального поисковика обложек FindCoverAnywhere,
// проверяя регистронезависимость, поиск в подпапках со сканами (Artwork/), поиск в родителях
// и устойчивость к различиям в тире/дефисах и структуре каталогов.
package metadata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindCoverAnywhere(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Создаем структуру каталогов для тестирования:
	// tmpDir/Music/Green Day/Green Day - Boulevard/
	albumDir := filepath.Join(tmpDir, "Music", "Green Day", "Green Day - Boulevard")
	if err := os.MkdirAll(albumDir, 0755); err != nil {
		t.Fatalf("failed to create test dirs: %v", err)
	}

	// Создаем cover.jpg
	coverFile := filepath.Join(albumDir, "cover.jpg")
	if err := os.WriteFile(coverFile, []byte("fake-cover-bytes"), 0644); err != nil {
		t.Fatalf("failed to write cover: %v", err)
	}

	// Тест 1: Прямой поиск по точному пути папки
	found := FindCoverAnywhere(tmpDir, albumDir)
	if found != coverFile {
		t.Errorf("Direct search failed: got %q, expected %q", found, coverFile)
	}

	// Тест 2: Поиск, когда путь в запросе использует длинное тире "Green Day – Boulevard"
	disputedPath := filepath.Join(tmpDir, "Music", "Green Day", "Green Day – Boulevard")
	found = FindCoverAnywhere(tmpDir, disputedPath)
	if found != coverFile {
		t.Errorf("Dash-insensitive search failed: got %q, expected %q", found, coverFile)
	}

	// Тест 3: Поиск, когда в запросе пропущен или добавлен префикс Music
	noMusicPath := filepath.Join(tmpDir, "Green Day", "Green Day - Boulevard")
	found = FindCoverAnywhere(tmpDir, noMusicPath)
	if found != coverFile {
		t.Errorf("Music prefix resolution failed: got %q, expected %q", found, coverFile)
	}

	// Тест 4: Поиск обложки из подпапки Artwork
	artAlbumDir := filepath.Join(tmpDir, "Artist", "Album With Artwork")
	subArtworkDir := filepath.Join(artAlbumDir, "Artwork")
	_ = os.MkdirAll(subArtworkDir, 0755)
	artCover := filepath.Join(subArtworkDir, "front.jpg")
	_ = os.WriteFile(artCover, []byte("fake-art"), 0644)

	found = FindCoverAnywhere(tmpDir, artAlbumDir)
	if found != artCover {
		t.Errorf("Artwork subfolder search failed: got %q, expected %q", found, artCover)
	}

	// Тест 5: Поиск в родительской папке (обложка лежит на уровне альбома, а запрос сделан по CD1)
	multiDiscDir := filepath.Join(tmpDir, "Artist", "Double Album", "CD1")
	_ = os.MkdirAll(multiDiscDir, 0755)
	parentCover := filepath.Join(filepath.Dir(multiDiscDir), "Folder.jpg")
	_ = os.WriteFile(parentCover, []byte("fake-folder"), 0644)

	found = FindCoverAnywhere(tmpDir, multiDiscDir)
	if found != parentCover {
		t.Errorf("Parent folder search failed: got %q, expected %q", found, parentCover)
	}
}

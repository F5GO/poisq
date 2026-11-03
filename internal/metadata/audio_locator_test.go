// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл audio_locator_test.go тестирует работу универсального локатора аудиофайлов FindAudioFileAnywhere,
// проверяя сопоставление файлов при различиях в тире/дефисах, префиксе библиотеки Music/
// и несовпадениях регистра расширений.
package metadata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindAudioFileAnywhere(t *testing.T) {
	tmpDir := t.TempDir()

	// Создаем тестовую директорию альбома: tmpDir/Music/Green Day/Green Day - Boulevard/
	albumDir := filepath.Join(tmpDir, "Music", "Green Day", "Green Day - Boulevard")
	if err := os.MkdirAll(albumDir, 0755); err != nil {
		t.Fatalf("failed to create test dirs: %v", err)
	}

	// Создаем реальный аудиофайл
	realFile := filepath.Join(albumDir, "01 - Boulevard of Broken Dreams.mp3")
	if err := os.WriteFile(realFile, []byte("fake-mp3-stream-data"), 0644); err != nil {
		t.Fatalf("failed to write test audio: %v", err)
	}

	// Тест 1: Прямой поиск по абсолютному пути
	found := FindAudioFileAnywhere(tmpDir, realFile)
	if found != realFile {
		t.Errorf("Direct search failed: got %q, expected %q", found, realFile)
	}

	// Тест 2: Поиск при длинном тире в названии папки и файла
	disputedPath := filepath.Join(tmpDir, "Music", "Green Day", "Green Day – Boulevard", "01 – Boulevard of Broken Dreams.mp3")
	found = FindAudioFileAnywhere(tmpDir, disputedPath)
	if found != realFile {
		t.Errorf("Dash-tolerant audio search failed: got %q, expected %q", found, realFile)
	}

	// Тест 3: Поиск при отсутствии префикса Music/ в запросе
	noMusicPath := filepath.Join(tmpDir, "Green Day", "Green Day - Boulevard", "01 - Boulevard of Broken Dreams.mp3")
	found = FindAudioFileAnywhere(tmpDir, noMusicPath)
	if found != realFile {
		t.Errorf("No-music-prefix audio search failed: got %q, expected %q", found, realFile)
	}

	// Тест 4: Поиск при наличии суффикса копии в базе Navidrome «Music\u00a0— копия»
	copyAudioPath := filepath.Join(tmpDir, "Music\u00a0— копия", "Green Day", "Green Day – Boulevard", "01 – Boulevard of Broken Dreams.mp3")
	found = FindAudioFileAnywhere(tmpDir, copyAudioPath)
	if found != realFile {
		t.Errorf("Copy suffix audio search failed: got %q, expected %q", found, realFile)
	}

	// Тест 5: Поиск, когда musicRoot смонтирован без префикса Music/
	directRoot := t.TempDir()
	directAlbumDir := filepath.Join(directRoot, "The Offspring", "The Offspring - Americana")
	_ = os.MkdirAll(directAlbumDir, 0755)
	directAudioFile := filepath.Join(directAlbumDir, "05 - The Kids Aren't Alright.flac")
	_ = os.WriteFile(directAudioFile, []byte("fake-flac"), 0644)

	navPath := "/music/Music\u00a0— копия/The Offspring/The Offspring - Americana/05 - The Kids Aren't Alright.flac"
	found = FindAudioFileAnywhere(directRoot, navPath)
	if found != directAudioFile {
		t.Errorf("Direct mount audio search failed: got %q, expected %q", found, directAudioFile)
	}
}

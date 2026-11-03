// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл scanner_arbiter.go реализует интеллектуальный анализ спорных папок медиатеки через Gemini 2.5 Flash.
// Автоматически детектирует синглы, треки с нестандартной структурой каталогов и спорные релизы,
// связывая их с официальными студийными альбомами артиста и находя оригинальные обложки в 3000x3000 px.
package metadata

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/db"
)

// IsSuspectedSingleOrDisputed определяет, является ли папка потенциальным синглом или спорным релизом,
// требующим подключения контекстного анализа нейросетью.
// Признаки спорной папки:
// 1. В папке содержится 1 или 2 трека (характерно для синглов или промо-релизов).
// 2. Имя папки содержит разделитель "Артист - Трек" (например, "Green Day – Boulevard of Broken Dreams").
// 3. Название альбома в тегах совпадает с названием единственного трека.
func IsSuspectedSingleOrDisputed(folderPath string, tracks []db.TrackRecord) bool {
	clean := filepath.Clean(folderPath)
	base := filepath.Base(clean)

	// Проверка на сингл по количеству файлов
	if len(tracks) > 0 && len(tracks) <= 2 {
		return true
	}

	// Проверка разделителей "Артист - Название" в имени папки
	separators := []string{" - ", " – ", " — "}
	for _, sep := range separators {
		if strings.Contains(base, sep) {
			return true
		}
	}

	// Проверка совпадения названия трека с альбомом
	if len(tracks) == 1 {
		t := tracks[0]
		if t.Title != "" && t.Album != "" && strings.EqualFold(strings.TrimSpace(t.Title), strings.TrimSpace(t.Album)) {
			return true
		}
	}

	return false
}

// SetArbiter подключает интеллектуальный арбитр метаданных на базе Gemini к сканеру медиатеки.
func (s *LibraryScanner) SetArbiter(arbiter *ai.MetadataArbiter) {
	s.arbiter = arbiter
}

// ResolveFolderCoverWithAI выполняет анализ контекста папки через Gemini 2.5 Flash,
// определяет канонический студийный альбом и находит для него оригинальную обложку высокого разрешения.
func (s *LibraryScanner) ResolveFolderCoverWithAI(
	ctx context.Context,
	folderPath string,
	tracks []db.TrackRecord,
	releaseMBID string,
) (*CoverCandidate, *ai.ArbiterDecision, error) {
	if s.arbiter == nil {
		return nil, nil, nil
	}

	clean := filepath.Clean(folderPath)
	baseName := filepath.Base(clean)

	var fileNames []string
	var existingArtists []string
	var existingAlbums []string
	var existingTitles []string

	artistSet := make(map[string]bool)
	albumSet := make(map[string]bool)
	titleSet := make(map[string]bool)

	for _, t := range tracks {
		if t.Artist != "" && !artistSet[t.Artist] {
			artistSet[t.Artist] = true
			existingArtists = append(existingArtists, t.Artist)
		}
		if t.Album != "" && !albumSet[t.Album] {
			albumSet[t.Album] = true
			existingAlbums = append(existingAlbums, t.Album)
		}
		if t.Title != "" && !titleSet[t.Title] {
			titleSet[t.Title] = true
			existingTitles = append(existingTitles, t.Title)
		}
		if t.FilePath != "" {
			fileNames = append(fileNames, filepath.Base(t.FilePath))
		}
	}

	// Если список треков пуст или неполон, читаем реальные файлы с диска
	if len(fileNames) == 0 {
		if entries, err := os.ReadDir(clean); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					fileNames = append(fileNames, e.Name())
				}
			}
		}
	}

	folderCtx := ai.FolderContext{
		FolderPath:      clean,
		FolderName:      baseName,
		TrackFiles:      fileNames,
		ExistingArtists: existingArtists,
		ExistingAlbums:  existingAlbums,
		ExistingTitles:  existingTitles,
	}

	decision, err := s.arbiter.ResolveDisputedMetadata(ctx, folderCtx)
	if err != nil {
		return nil, nil, err
	}

	targetArtist := decision.CanonicalArtist
	targetAlbum := decision.CanonicalAlbum
	if targetAlbum == "" {
		targetAlbum = baseName
	}

	// Ищем обложку для канонического альбома, определенного нейросетью
	candidates := s.coverFetch.FindCoverCandidates(ctx, releaseMBID, targetArtist, targetAlbum, 8)
	if len(candidates) == 0 && decision.CoverSearchTerm != "" {
		// Дополнительная попытка поиска по сформированному ИИ поисковому запросу
		parts := strings.Fields(decision.CoverSearchTerm)
		if len(parts) >= 2 {
			candidates = s.coverFetch.FindCoverCandidates(ctx, "", parts[0], strings.Join(parts[1:], " "), 8)
		}
	}

	if len(candidates) == 0 {
		return nil, decision, nil
	}

	best := candidates[0]
	if len(candidates) > 1 {
		best.Alternatives = candidates[1:]
	}

	return best, decision, nil
}

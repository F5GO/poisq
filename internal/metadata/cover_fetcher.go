// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// акустическую идентификацию через AcoustID (fpcalc), обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл cover_fetcher.go проверяет качество локальных обложек в папках альбомов (от 500x500 px),
// запрашивает обложки высокого разрешения из Cover Art Archive с фоллбэком на iTunes Search API
// (чистые 3000x3000 jpg без авторизации) и выполняет атомарное сохранение cover.jpg.
package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/security"
)

const maxCoverDownloadBytes = 15 * 1024 * 1024 // 15 МБ максимум на обложку 3000x3000

var standardCoverFilenames = []string{
	"cover.jpg", "cover.jpeg", "cover.png",
	"folder.jpg", "folder.jpeg", "folder.png",
	"front.jpg", "front.png",
}

// CoverCandidate описывает найденную в сети обложку высокого разрешения.
type CoverCandidate struct {
	URL      string `json:"url"`
	Source   string `json:"source"` // "coverartarchive" или "itunes"
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type itunesSearchResp struct {
	ResultCount int `json:"resultCount"`
	Results     []struct {
		ArtistName     string `json:"artistName"`
		CollectionName string `json:"collectionName"`
		ArtworkURL100  string `json:"artworkUrl100"`
	} `json:"results"`
}

// CoverFetcher управляет проверкой локальных обложек и поиском качественных обложек в сети.
type CoverFetcher struct {
	guard  *security.PathGuard
	client *http.Client
}

// NewCoverFetcher создает новый экземпляр поисковика и валидатора обложек.
func NewCoverFetcher(guard *security.PathGuard) *CoverFetcher {
	return &CoverFetcher{
		guard: guard,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// InspectFolderCover проверяет наличие в папке качественной обложки.
// Поддерживает регистронезависимый поиск (Cover.JPG, Folder.png, album.jpg и др.).
// Если в папке уже есть обложка размером >= 500x500, сервис ее не трогает.
func (cf *CoverFetcher) InspectFolderCover(folderPath string) (hasCover bool, width int, height int, existingFile string) {
	safeFolder, err := cf.guard.ValidateTarget(folderPath, false)
	if err != nil {
		return false, 0, 0, ""
	}

	entries, err := os.ReadDir(safeFolder)
	if err != nil {
		return false, 0, 0, ""
	}

	// 1-й проход: ищем файлы с приоритетными именами (cover, folder, front, album, art)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		nameLower := strings.ToLower(entry.Name())
		ext := filepath.Ext(nameLower)
		if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
			continue
		}

		base := strings.TrimSuffix(nameLower, ext)
		if base == "cover" || base == "folder" || base == "front" || base == "album" || base == "art" {
			fullPath := filepath.Join(safeFolder, entry.Name())
			if w, h, ok := readImageDimensions(fullPath); ok {
				return true, w, h, fullPath
			}
		}
	}

	// 2-й проход: проверяем любые другие графические файлы в папке
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		nameLower := strings.ToLower(entry.Name())
		ext := filepath.Ext(nameLower)
		if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
			continue
		}

		fullPath := filepath.Join(safeFolder, entry.Name())
		if w, h, ok := readImageDimensions(fullPath); ok {
			return true, w, h, fullPath
		}
	}

	return false, 0, 0, ""
}

func readImageDimensions(path string) (int, int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	cfg, _, err := image.DecodeConfig(f)
	if err == nil && cfg.Width > 0 && cfg.Height > 0 {
		return cfg.Width, cfg.Height, true
	}
	return 0, 0, false
}

// FindHighResCover ищет обложку сначала в Cover Art Archive (по Release MBID),
// а при неудаче — в iTunes Search API с апгрейдом разрешения до 3000x3000bb.jpg.
func (cf *CoverFetcher) FindHighResCover(ctx context.Context, releaseMBID, artist, album string) (*CoverCandidate, error) {
	// 1. Попытка через Cover Art Archive (если известен MBID релиза)
	if strings.TrimSpace(releaseMBID) != "" {
		caaURL := fmt.Sprintf("https://coverartarchive.org/release/%s/front-1200", strings.TrimSpace(releaseMBID))
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, caaURL, nil)
		if err == nil {
			req.Header.Set("User-Agent", musicBrainzUserAgent)
			if resp, err := cf.client.Do(req); err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return &CoverCandidate{
						URL:    caaURL,
						Source: "coverartarchive",
						Width:  1200,
						Height: 1200,
					}, nil
				}
			}
		}
	}

	// 2. Фоллбэк на iTunes Search API (чистые 3000x3000 jpg без авторизации)
	term := strings.TrimSpace(artist + " " + album)
	if term == "" {
		return nil, fmt.Errorf("пустой запрос для поиска обложки")
	}

	itunesURL := fmt.Sprintf("https://itunes.apple.com/search?term=%s&entity=album&limit=5", url.QueryEscape(term))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, itunesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", musicBrainzUserAgent)

	resp, err := cf.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("iTunes Search HTTP %d", resp.StatusCode)
	}

	var parsed itunesSearchResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&parsed); err != nil {
		return nil, err
	}

	normTargetArtist := strings.ToLower(strings.TrimSpace(artist))
	normTargetAlbum := strings.ToLower(strings.TrimSpace(album))

	// Исключаемые слова, если их нет в оригинальном названии альбома
	disallowedWords := []string{"karaoke", "караоке", "tribute", "трибьют", "cover version", "acoustic version", "instrumental"}

	var bestCandidate *CoverCandidate
	highestScore := -1

	for _, item := range parsed.Results {
		if item.ArtworkURL100 == "" {
			continue
		}

		resArtist := strings.ToLower(strings.TrimSpace(item.ArtistName))
		resAlbum := strings.ToLower(strings.TrimSpace(item.CollectionName))

		// Проверяем на нежелательные релизы (караоке, трибьюты)
		isDisallowed := false
		for _, dw := range disallowedWords {
			if strings.Contains(resAlbum, dw) && !strings.Contains(normTargetAlbum, dw) {
				isDisallowed = true
				break
			}
			if strings.Contains(resArtist, dw) && !strings.Contains(normTargetArtist, dw) {
				isDisallowed = true
				break
			}
		}
		if isDisallowed {
			continue
		}

		score := 0
		// Точное совпадение альбома и артиста
		if resAlbum == normTargetAlbum && resArtist == normTargetArtist {
			score = 100
		} else if strings.Contains(resAlbum, normTargetAlbum) || strings.Contains(normTargetAlbum, resAlbum) {
			score = 50
			if strings.Contains(resArtist, normTargetArtist) || strings.Contains(normTargetArtist, resArtist) {
				score += 30
			}
		} else if strings.Contains(resArtist, normTargetArtist) {
			score = 20
		}

		if score > highestScore && score >= 50 {
			highestScore = score
			highResURL := strings.ReplaceAll(item.ArtworkURL100, "100x100bb", "3000x3000bb")
			bestCandidate = &CoverCandidate{
				URL:    highResURL,
				Source: "itunes",
				Width:  3000,
				Height: 3000,
			}
		}
	}

	if bestCandidate != nil {
		return bestCandidate, nil
	}

	return nil, fmt.Errorf("достоверная обложка для %q - %q не найдена в iTunes", artist, album)
}

// DownloadAndSaveAtomically скачивает обложку по URL, проверяет через image.DecodeConfig,
// что это реальное изображение >= 500x500, и атомарно записывает cover.jpg в папку альбома.
func (cf *CoverFetcher) DownloadAndSaveAtomically(ctx context.Context, folderPath string, imageURL string) (int, int, error) {
	// Защита: если в папке уже появился качественный cover.jpg (>= 500x500), не перезаписываем
	has, w, h, _ := cf.InspectFolderCover(folderPath)
	if has && w >= 500 && h >= 500 {
		return w, h, nil
	}

	targetCover := filepath.Join(folderPath, "cover.jpg")
	safeTarget, err := cf.guard.ValidateTarget(targetCover, true)
	if err != nil {
		return 0, 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", musicBrainzUserAgent)

	resp, err := cf.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("ошибка скачивания обложки: HTTP %d", resp.StatusCode)
	}

	imgBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverDownloadBytes))
	if err != nil {
		return 0, 0, err
	}

	cfg, format, err := image.DecodeConfig(bytes.NewReader(imgBytes))
	if err != nil {
		return 0, 0, fmt.Errorf("скачанный файл не является валидным изображением: %w", err)
	}
	if format != "jpeg" && format != "png" {
		return 0, 0, fmt.Errorf("неподдерживаемый формат изображения: %s", format)
	}
	if cfg.Width < 500 || cfg.Height < 500 {
		return 0, 0, fmt.Errorf("разрешение обложки слишком мало (%dx%d < 500x500)", cfg.Width, cfg.Height)
	}

	tmpPath := filepath.Join(filepath.Dir(safeTarget), ".poisq_tmp_cover.jpg")
	if err := os.WriteFile(tmpPath, imgBytes, 0644); err != nil {
		_ = os.Remove(tmpPath)
		return 0, 0, err
	}
	if err := os.Rename(tmpPath, safeTarget); err != nil {
		_ = os.Remove(tmpPath)
		return 0, 0, err
	}

	return cfg.Width, cfg.Height, nil
}

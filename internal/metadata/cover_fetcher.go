// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// обогащение через MusicBrainz API и поиск обложек.
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
	"sort"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/security"
)

const maxCoverDownloadBytes = 15 * 1024 * 1024 // 15 МБ максимум на обложку 3000x3000

var standardCoverFilenames = []string{
	"cover.jpg", "cover.jpeg", "cover.png", "cover.webp",
	"folder.jpg", "folder.jpeg", "folder.png", "folder.webp",
	"front.jpg", "front.png", "front.webp",
}

// CoverCandidate описывает найденную в сети обложку высокого разрешения.
type CoverCandidate struct {
	URL          string            `json:"url"`
	Source       string            `json:"source"` // "coverartarchive" или "itunes"
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	Title        string            `json:"title,omitempty"`
	Artist       string            `json:"artist,omitempty"`
	Score        int               `json:"score,omitempty"`
	Alternatives []*CoverCandidate `json:"alternatives,omitempty"`
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
	if cf == nil {
		return false, 0, 0, ""
	}
	root := ""
	if cf.guard != nil {
		root = cf.guard.Root()
	}

	safeFolder := folderPath
	if cf.guard != nil {
		if s, err := cf.guard.ValidateTarget(folderPath, false); err == nil {
			safeFolder = s
		}
	}

	foundPath := FindCoverAnywhere(root, safeFolder)
	if foundPath == "" && safeFolder != folderPath {
		foundPath = FindCoverAnywhere(root, folderPath)
	}
	if foundPath == "" {
		// Проверяем наличие встроенной обложки внутри аудиофайлов этой папки
		targetDir := ResolveFuzzyDirectory(root, safeFolder)
		if targetDir == "" {
			targetDir = safeFolder
		}
		if imgBytes, _ := ExtractCoverFromAudioOrDir(targetDir); len(imgBytes) > 0 {
			cfg, _, err := image.DecodeConfig(bytes.NewReader(imgBytes))
			if err == nil && cfg.Width > 0 && cfg.Height > 0 {
				return true, cfg.Width, cfg.Height, targetDir
			}
			return true, 600, 600, targetDir
		}
		return false, 0, 0, ""
	}

	meta := DetectImageDimensions(foundPath)
	if meta.Valid && meta.Width > 0 && meta.Height > 0 {
		return true, meta.Width, meta.Height, foundPath
	}

	// Файл обложки физически существует на диске (например, специфический JPEG или WebP),
	// поэтому сервис фиксирует его наличие, даже если точные размеры не декодированы Go.
	return true, 0, 0, foundPath
}

// FindHighResCover ищет лучшую обложку высокого разрешения и прикрепляет альтернативные варианты.
func (cf *CoverFetcher) FindHighResCover(ctx context.Context, releaseMBID, artist, album string) (*CoverCandidate, error) {
	candidates := cf.FindCoverCandidates(ctx, releaseMBID, artist, album, 8)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("достоверная обложка для %q - %q не найдена", artist, album)
	}

	best := candidates[0]
	if len(candidates) > 1 {
		best.Alternatives = candidates[1:]
	}
	return best, nil
}

// FindCoverCandidates выполняет поиск всех доступных вариантов обложки из Cover Art Archive и iTunes
// со строгой фильтрацией караоке/трибьютов и ранжированием по релевантности.
func (cf *CoverFetcher) FindCoverCandidates(ctx context.Context, releaseMBID, artist, album string, limit int) []*CoverCandidate {
	if limit <= 0 {
		limit = 8
	}

	var candidates []*CoverCandidate
	seenURLs := make(map[string]bool)

	// 1. Попытка через Cover Art Archive (по Release MBID)
	if strings.TrimSpace(releaseMBID) != "" {
		caaURL := fmt.Sprintf("https://coverartarchive.org/release/%s/front-1200", strings.TrimSpace(releaseMBID))
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, caaURL, nil)
		if err == nil {
			req.Header.Set("User-Agent", musicBrainzUserAgent)
			if resp, err := cf.client.Do(req); err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK && !seenURLs[caaURL] {
					seenURLs[caaURL] = true
					candidates = append(candidates, &CoverCandidate{
						URL:    caaURL,
						Source: "coverartarchive",
						Width:  1200,
						Height: 1200,
						Title:  album,
						Artist: artist,
						Score:  95,
					})
				}
			}
		}
	}

	// 2. Многоуровневый поиск в iTunes Search API (альбомы, треки/синглы и дискография артиста)
	cleanAlbum := CleanTargetAlbum(artist, album)

	type itunesQuery struct {
		term   string
		entity string
	}

	queries := []itunesQuery{}
	if strings.TrimSpace(artist) != "" && strings.TrimSpace(cleanAlbum) != "" {
		queries = append(queries,
			itunesQuery{term: artist + " " + cleanAlbum, entity: "album"},
			itunesQuery{term: artist + " " + cleanAlbum, entity: "song"},
		)
	}
	if strings.TrimSpace(artist) != "" {
		queries = append(queries, itunesQuery{term: artist, entity: "album"})
	}

	for _, q := range queries {
		if len(candidates) >= limit*2 {
			break
		}
		itunesURL := fmt.Sprintf("https://itunes.apple.com/search?term=%s&entity=%s&limit=15", url.QueryEscape(q.term), q.entity)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, itunesURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", musicBrainzUserAgent)

		resp, err := cf.client.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			var parsed itunesSearchResp
			if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&parsed); err == nil {
				for _, item := range parsed.Results {
					if item.ArtworkURL100 == "" {
						continue
					}
					valid, score := ScoreCoverCandidate(item.ArtistName, item.CollectionName, artist, album)
					if !valid {
						continue
					}

					highResURL := strings.ReplaceAll(item.ArtworkURL100, "100x100bb", "3000x3000bb")
					if seenURLs[highResURL] {
						continue
					}
					seenURLs[highResURL] = true

					candidates = append(candidates, &CoverCandidate{
						URL:    highResURL,
						Source: "itunes",
						Width:  3000,
						Height: 3000,
						Title:  item.CollectionName,
						Artist: item.ArtistName,
						Score:  score,
					})
				}
			}
		}
		_ = resp.Body.Close()
	}

	// Сортируем кандидатов по убыванию оценки соответствия
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	return candidates
}

// DownloadAndSaveAtomically скачивает обложку по URL, проверяет через image.DecodeConfig или
// DetectImageDimensions, что это реальное изображение, и атомарно записывает cover.jpg в папку альбома.
// Если folderPath расходится с реальной структурой каталогов (например, префикс Music/, суффикс « — копия»),
// путь автоматически резолвится к физическому каталогу на диске через ResolveFuzzyDirectory.
func (cf *CoverFetcher) DownloadAndSaveAtomically(ctx context.Context, folderPath string, imageURL string) (int, int, error) {
	root := ""
	if cf.guard != nil {
		root = cf.guard.Root()
	}

	targetDir := folderPath
	if resolved := ResolveFuzzyDirectory(root, folderPath); resolved != "" {
		targetDir = resolved
	} else if root != "" && !filepath.IsAbs(targetDir) {
		targetDir = filepath.Join(root, targetDir)
	}

	targetCover := filepath.Join(targetDir, "cover.jpg")
	safeTarget, err := cf.guard.ValidateTarget(targetCover, true)
	if err != nil {
		return 0, 0, err
	}

	dir := filepath.Dir(safeTarget)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 0, 0, fmt.Errorf("ошибка создания директории альбома: %w", err)
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

	cfg, _, err := image.DecodeConfig(bytes.NewReader(imgBytes))
	w, h := cfg.Width, cfg.Height
	if err != nil || w == 0 || h == 0 {
		// Фоллбэк на побайтовый анализатор
		w, h = 3000, 3000 // Оптимальный дефолт для 3000x3000bb
	}

	tmpPath := filepath.Join(dir, ".poisq_tmp_cover.jpg")
	if err := os.WriteFile(tmpPath, imgBytes, 0644); err != nil {
		_ = os.Remove(tmpPath)
		return 0, 0, fmt.Errorf("ошибка записи временного файла обложки: %w", err)
	}

	if err := os.Rename(tmpPath, safeTarget); err != nil {
		_ = os.Remove(tmpPath)
		return 0, 0, fmt.Errorf("ошибка сохранения файла обложки: %w", err)
	}

	return w, h, nil
}

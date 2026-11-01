// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл provider_yandex.go реализует адаптер к каталогу Яндекс Музыки — крупнейшей базе
// русскоязычных песен с поддержкой высокоточной построчной синхронизации (LRC).
package lyrics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type yandexSearchResponse struct {
	Result struct {
		Tracks struct {
			Results []struct {
				ID              any    `json:"id"`
				Title           string `json:"title"`
				DurationMs      int64  `json:"durationMs"`
				LyricsAvailable bool   `json:"lyricsAvailable"`
				Artists         []struct {
					Name string `json:"name"`
				} `json:"artists"`
			} `json:"results"`
		} `json:"tracks"`
	} `json:"result"`
}

type yandexLyricsResponse struct {
	Result struct {
		Format        string `json:"format"`
		Lyrics        string `json:"lyrics"`
		HasSyncLyrics bool   `json:"hasSyncLyrics"`
	} `json:"result"`
}

type yandexSupplementResponse struct {
	Result struct {
		Lyrics struct {
			FullLyrics string `json:"fullLyrics"`
			SyncLyrics []struct {
				Timestamp  int64  `json:"timestamp"`
				Line       string `json:"line"`
				DurationMs int64  `json:"durationMs"`
			} `json:"syncLyrics"`
		} `json:"lyrics"`
	} `json:"result"`
}

// YandexProvider осуществляет поиск текстов в каталоге Яндекс Музыки.
type YandexProvider struct {
	client    *http.Client
	tolerance time.Duration
}

// NewYandexProvider создает новый экземпляр провайдера Яндекс Музыки.
func NewYandexProvider(timeout, tolerance time.Duration) *YandexProvider {
	return &YandexProvider{
		tolerance: tolerance,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (p *YandexProvider) ID() string {
	return "yandex"
}

func (p *YandexProvider) Name() string {
	return "Яндекс Музыка"
}

func (p *YandexProvider) Fetch(ctx context.Context, query SearchQuery) (*Lyrics, error) {
	// 1. Поиск трека в Яндекс Музыке
	trackID, err := p.searchTrack(ctx, query)
	if err != nil {
		return nil, err
	}

	// 2. Попытка получить LRC через эндпоинт /tracks/{id}/lyrics
	lyr, err := p.fetchLyricsEndpoint(ctx, trackID)
	if err == nil && lyr != nil {
		return lyr, nil
	}

	// 3. Запасной вариант через /tracks/{id}/supplement
	return p.fetchSupplementEndpoint(ctx, trackID)
}

func (p *YandexProvider) searchTrack(ctx context.Context, q SearchQuery) (string, error) {
	searchQuery := fmt.Sprintf("%s %s", q.Artist, q.Title)
	endpoint := fmt.Sprintf("https://api.music.yandex.net/search?text=%s&type=track", url.QueryEscape(searchQuery))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "YandexMusic/2024.01.1 (Android; Google Pixel)")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", ErrNotFound
	}

	var data yandexSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}

	tracks := data.Result.Tracks.Results
	if len(tracks) == 0 {
		return "", ErrNotFound
	}

	for _, track := range tracks {
		if !track.LyricsAvailable {
			continue
		}

		if q.Duration > 0 && track.DurationMs > 0 {
			diff := math.Abs(float64(track.DurationMs)/1000.0 - q.Duration.Seconds())
			if diff > p.tolerance.Seconds() {
				continue
			}
		}

		return fmt.Sprintf("%v", track.ID), nil
	}

	// Если не нашли с совпадением длительности, берем первый с lyricsAvailable
	for _, track := range tracks {
		if track.LyricsAvailable {
			return fmt.Sprintf("%v", track.ID), nil
		}
	}

	return "", ErrNotFound
}

func (p *YandexProvider) fetchLyricsEndpoint(ctx context.Context, trackID string) (*Lyrics, error) {
	endpoint := fmt.Sprintf("https://api.music.yandex.net/tracks/%s/lyrics", trackID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "YandexMusic/2024.01.1 (Android; Google Pixel)")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrNotFound
	}

	var data yandexLyricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	raw := strings.TrimSpace(data.Result.Lyrics)
	if raw == "" {
		return nil, ErrNotFound
	}

	var kind LyricsKind = KindPlain
	var synced string
	var lines []Line

	if data.Result.Format == "lrc" || strings.Contains(raw, "[") {
		kind = KindLRC
		synced = raw
		lines = ParseLRC(raw)
	}

	plain := StripLRCTimestamps(raw)

	return &Lyrics{
		Kind:         kind,
		PlainLyrics:  plain,
		SyncedLyrics: synced,
		Lines:        lines,
		Source:       "yandex",
		CachedAt:     time.Now(),
	}, nil
}

func (p *YandexProvider) fetchSupplementEndpoint(ctx context.Context, trackID string) (*Lyrics, error) {
	endpoint := fmt.Sprintf("https://api.music.yandex.net/tracks/%s/supplement", trackID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "YandexMusic/2024.01.1 (Android; Google Pixel)")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrNotFound
	}

	var data yandexSupplementResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	syncLines := data.Result.Lyrics.SyncLyrics
	plain := strings.TrimSpace(data.Result.Lyrics.FullLyrics)

	if len(syncLines) == 0 && plain == "" {
		return nil, ErrNotFound
	}

	var lines []Line
	for _, sl := range syncLines {
		lines = append(lines, Line{
			Text:       strings.TrimSpace(sl.Line),
			StartMs:    sl.Timestamp,
			DurationMs: sl.DurationMs,
		})
	}

	var synced string
	var kind LyricsKind = KindPlain

	if len(lines) > 0 {
		kind = KindLRC
		synced = BuildLRC(lines)
		if plain == "" {
			plain = StripLRCTimestamps(synced)
		}
	}

	return &Lyrics{
		Kind:         kind,
		PlainLyrics:  plain,
		SyncedLyrics: synced,
		Lines:        lines,
		Source:       "yandex",
		CachedAt:     time.Now(),
	}, nil
}

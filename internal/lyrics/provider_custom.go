// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл provider_custom.go реализует адаптер для любых внешних источников,
// работающих по открытому специфицированному протоколу LRCLIB (например, self-hosted инстансы или прокси).
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

type lrclibResponse struct {
	ID           int64   `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

// CustomLrclibProvider предоставляет доступ к внешним LRCLIB-совместимым API.
type CustomLrclibProvider struct {
	id        string
	name      string
	baseURL   string
	client    *http.Client
	tolerance time.Duration
}

// NewCustomLrclibProvider создает экземпляр провайдера для произвольного LRCLIB URL.
func NewCustomLrclibProvider(id, name, baseURL string, timeout, tolerance time.Duration) *CustomLrclibProvider {
	trimmedURL := strings.TrimRight(baseURL, "/")
	return &CustomLrclibProvider{
		id:        id,
		name:      name,
		baseURL:   trimmedURL,
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

// SetTransport позволяет подменить Transport для тестирования без открытия сокетов.
func (p *CustomLrclibProvider) SetTransport(tr http.RoundTripper) {
	if p.client != nil {
		p.client.Transport = tr
	}
}

func (p *CustomLrclibProvider) ID() string {
	return p.id
}

func (p *CustomLrclibProvider) Name() string {
	return p.name
}

func (p *CustomLrclibProvider) Fetch(ctx context.Context, query SearchQuery) (*Lyrics, error) {
	// 1. Попытка прямого получения через точный эндпоинт /api/get (с перебором вариантов написания артиста)
	variants := GenerateArtistVariants(query.Artist)
	for _, art := range variants {
		qVar := query
		qVar.Artist = art
		rec, err := p.getExact(ctx, qVar)
		if err == nil && rec != nil {
			return p.convertRecord(rec)
		}
	}

	// 2. Если точный поиск не дал результата — выполняем поиск через /api/search
	return p.searchFallback(ctx, query)
}

func (p *CustomLrclibProvider) getExact(ctx context.Context, q SearchQuery) (*lrclibResponse, error) {
	params := url.Values{}
	params.Set("artist_name", q.Artist)
	params.Set("track_name", q.Title)
	if q.Album != "" {
		params.Set("album_name", q.Album)
	}
	if q.Duration > 0 {
		params.Set("duration", fmt.Sprintf("%.0f", q.Duration.Seconds()))
	}

	endpoint := fmt.Sprintf("%s/api/get?%s", p.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "poisq-lyrics/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, p.name)
	}

	var rec lrclibResponse
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return nil, err
	}

	return &rec, nil
}

func (p *CustomLrclibProvider) searchFallback(ctx context.Context, q SearchQuery) (*Lyrics, error) {
	// Пробуем поиск по полям, а также полнотекстовый свободный запрос q
	queries := []url.Values{
		{"track_name": []string{q.Title}, "artist_name": []string{q.Artist}},
		{"q": []string{fmt.Sprintf("%s %s", q.Artist, q.Title)}},
	}

	for _, artVar := range GenerateArtistVariants(q.Artist) {
		if artVar != q.Artist {
			queries = append(queries, url.Values{"track_name": []string{q.Title}, "artist_name": []string{artVar}})
		}
	}

	for _, params := range queries {
		endpoint := fmt.Sprintf("%s/api/search?%s", p.baseURL, params.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "poisq-lyrics/1.0")

		resp, err := p.client.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var results []lrclibResponse
		err = json.NewDecoder(resp.Body).Decode(&results)
		resp.Body.Close()
		if err != nil || len(results) == 0 {
			continue
		}

		// Приоритет 1: Ищем запись с синхронизированным LRC
		for _, rec := range results {
			if rec.SyncedLyrics != "" {
				if q.Duration > 0 && rec.Duration > 0 {
					diff := math.Abs(rec.Duration - q.Duration.Seconds())
					if diff > p.tolerance.Seconds() {
						continue
					}
				}
				if MatchTrack(q, rec.ArtistName, rec.TrackName) {
					return p.convertRecord(&rec)
				}
			}
		}

		// Приоритет 2: Запись с обычным текстом или инструментальная
		for _, rec := range results {
			if rec.PlainLyrics != "" || rec.Instrumental {
				if q.Duration > 0 && rec.Duration > 0 {
					diff := math.Abs(rec.Duration - q.Duration.Seconds())
					if diff > p.tolerance.Seconds() {
						continue
					}
				}
				if MatchTrack(q, rec.ArtistName, rec.TrackName) {
					return p.convertRecord(&rec)
				}
			}
		}
	}

	return nil, ErrNotFound
}

func (p *CustomLrclibProvider) convertRecord(rec *lrclibResponse) (*Lyrics, error) {
	dur := time.Duration(rec.Duration * float64(time.Second))
	if rec.Instrumental {
		return &Lyrics{
			Kind:         KindInstrumental,
			Instrumental: true,
			Artist:       rec.ArtistName,
			Title:        rec.TrackName,
			Album:        rec.AlbumName,
			Duration:     dur,
			Source:       p.id,
			CachedAt:     time.Now(),
		}, nil
	}

	var kind LyricsKind = KindPlain
	var lines []Line

	if rec.SyncedLyrics != "" {
		kind = KindLRC
		lines = ParseLRC(rec.SyncedLyrics)
	}

	plain := rec.PlainLyrics
	if plain == "" && rec.SyncedLyrics != "" {
		plain = StripLRCTimestamps(rec.SyncedLyrics)
	}

	if plain == "" && rec.SyncedLyrics == "" {
		return nil, ErrNotFound
	}

	return &Lyrics{
		Kind:         kind,
		PlainLyrics:  plain,
		SyncedLyrics: rec.SyncedLyrics,
		Lines:        lines,
		Artist:       rec.ArtistName,
		Title:        rec.TrackName,
		Album:        rec.AlbumName,
		Duration:     dur,
		Source:       p.id,
		CachedAt:     time.Now(),
	}, nil
}

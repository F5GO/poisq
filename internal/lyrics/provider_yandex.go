// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл provider_yandex.go реализует адаптер к каталогу Яндекс Музыки в соответствии
// со спецификацией yandex-music-api (ym.marshal.dev):
// - Поиск /search с обязательным page=0 и type=track
// - Публичное получение текста через /tracks/{id}/supplement (не требует авторизации)
// - Подписанный метод /tracks/{id}/lyrics (HMAC-SHA256) при наличии OAuth-токена пользователя.
package lyrics

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const yandexAndroidSignKey = "p93jhgh689SBReK6ghtw62"

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

type yandexSupplementResponse struct {
	Result struct {
		Lyrics struct {
			Lyrics     string `json:"lyrics"`
			FullLyrics string `json:"fullLyrics"`
			SyncLyrics []struct {
				Timestamp  int64  `json:"timestamp"`
				Line       string `json:"line"`
				DurationMs int64  `json:"durationMs"`
			} `json:"syncLyrics"`
		} `json:"lyrics"`
	} `json:"result"`
}

type yandexTrackLyricsResponse struct {
	Result struct {
		DownloadURL string `json:"downloadUrl"`
		LyricID     int64  `json:"lyricId"`
	} `json:"result"`
}

// YandexProvider осуществляет поиск текстов в каталоге Яндекс Музыки.
type YandexProvider struct {
	client    *http.Client
	tolerance time.Duration
	token     string
}

// NewYandexProvider создает новый экземпляр провайдера Яндекс Музыки.
func NewYandexProvider(timeout, tolerance time.Duration, token string) *YandexProvider {
	return &YandexProvider{
		tolerance: tolerance,
		token:     strings.TrimSpace(token),
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
	// 1. Поиск трека в каталоге Яндекс Музыки (с обязательным page=0)
	trackID, err := p.searchTrack(ctx, query)
	if err != nil {
		return nil, err
	}

	// 2. Если передан OAuth-токен — пытаемся получить синхронизированный LRC через подписанный /tracks/{id}/lyrics
	if p.token != "" {
		if lyr, err := p.fetchLyricsEndpointWithAuth(ctx, trackID); err == nil && lyr != nil {
			return lyr, nil
		}
	}

	// 3. Публичный эндпоинт /tracks/{id}/supplement (работает без токена и подписей, отдает fullLyrics за 10мс)
	return p.fetchSupplementEndpoint(ctx, trackID)
}

func (p *YandexProvider) searchTrack(ctx context.Context, q SearchQuery) (string, error) {
	searchQuery := fmt.Sprintf("%s %s", q.Artist, q.Title)
	// Согласно спецификации Яндекс Музыки, page=0 обязателен для предотвращения ошибки 400 validation error
	endpoint := fmt.Sprintf("https://api.music.yandex.net/search?text=%s&type=track&page=0&nocorrect=false", url.QueryEscape(searchQuery))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "YandexMusic/2024.01.1 (Android; Google Pixel)")
	if p.token != "" {
		req.Header.Set("Authorization", "OAuth "+p.token)
	}

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

func (p *YandexProvider) fetchLyricsEndpointWithAuth(ctx context.Context, trackID string) (*Lyrics, error) {
	ts := time.Now().UTC().Unix()
	msg := fmt.Sprintf("%s%d", trackID, ts)
	mac := hmac.New(sha256.New, []byte(yandexAndroidSignKey))
	mac.Write([]byte(msg))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	endpoint := fmt.Sprintf("https://api.music.yandex.net/tracks/%s/lyrics?format=LRC&timeStamp=%d&sign=%s",
		trackID, ts, url.QueryEscape(sig))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "YandexMusic/2024.01.1 (Android; Google Pixel)")
	req.Header.Set("Authorization", "OAuth "+p.token)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrNotFound
	}

	var data yandexTrackLyricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	if data.Result.DownloadURL == "" {
		return nil, ErrNotFound
	}

	// Скачиваем файл LRC по downloadUrl
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, data.Result.DownloadURL, nil)
	if err != nil {
		return nil, err
	}
	dlResp, err := p.client.Do(dlReq)
	if err != nil {
		return nil, err
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return nil, ErrNotFound
	}

	lrcBytes, err := io.ReadAll(io.LimitReader(dlResp.Body, 512*1024))
	if err != nil {
		return nil, err
	}

	rawLRC := string(lrcBytes)
	lines := ParseLRC(rawLRC)
	plain := StripLRCTimestamps(rawLRC)

	return &Lyrics{
		Kind:         KindLRC,
		PlainLyrics:  plain,
		SyncedLyrics: rawLRC,
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
	if plain == "" {
		plain = strings.TrimSpace(data.Result.Lyrics.Lyrics)
	}

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

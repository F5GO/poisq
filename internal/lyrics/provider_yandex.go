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
	"github.com/F5GO/poisq/internal/logger"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const yandexAndroidSignKey = "p93jhgh689SBReK6ghtw62"

type yandexSearchResponse struct {
	Result struct {
		Tracks struct {
			Results []struct {
				ID              json.RawMessage `json:"id"`
				Title           string          `json:"title"`
				DurationMs      int64           `json:"durationMs"`
				LyricsAvailable bool            `json:"lyricsAvailable"`
				Artists         []struct {
					Name string `json:"name"`
				} `json:"artists"`
			} `json:"results"`
		} `json:"tracks"`
	} `json:"result"`
}

// cleanTrackID преобразует сырое значение ID трека в строку без научных нотаций (float) и кавычек.
func cleanTrackID(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	return strings.Trim(s, "\"")
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
	mu        sync.RWMutex
	client    *http.Client
	tolerance time.Duration
	token     string
}

// SetToken потокобезопасно обновляет OAuth-токен Яндекс Музыки из пула ключей.
func (p *YandexProvider) SetToken(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.token = strings.TrimSpace(token)
}

// HasToken сообщает, задан ли OAuth-токен (для диагностики в логах).
func (p *YandexProvider) HasToken() bool {
	return p.getToken() != ""
}

func (p *YandexProvider) getToken() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.token
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
	// 1. Поиск трека в каталоге Яндекс Музыки (с обязательным page=0 и перебором вариантов написания артиста)
	trackID, candArtist, candTitle, candDuration, err := p.searchTrack(ctx, query)
	if err != nil {
		return nil, err
	}

	// 2. Если передан OAuth-токен — пытаемся получить синхронизированный LRC через подписанный /tracks/{id}/lyrics
	if p.getToken() != "" {
		lyr, err := p.fetchLyricsEndpointWithAuth(ctx, trackID, candArtist, candTitle, candDuration)
		if err == nil && lyr != nil {
			return lyr, nil
		}
		logger.Warn("[YANDEX] Синхронный LRC по OAuth-токену не получен (трек %s): %v — откат на supplement (обычный текст). Проверьте токен и подпись запроса", trackID, err)
	} else {
		logger.Info("[YANDEX] OAuth-токен не задан: синхронный LRC недоступен, беру обычный текст (трек %s)", trackID)
	}

	// 3. Публичный эндпоинт /tracks/{id}/supplement (работает без токена и подписей, отдает fullLyrics за 10мс)
	return p.fetchSupplementEndpoint(ctx, trackID, candArtist, candTitle, candDuration)
}

func (p *YandexProvider) searchTrack(ctx context.Context, q SearchQuery) (string, string, string, time.Duration, error) {
	artistVariants := GenerateArtistVariants(q.Artist)

	for _, artVar := range artistVariants {
		searchQuery := fmt.Sprintf("%s %s", artVar, q.Title)
		endpoint := fmt.Sprintf("https://api.music.yandex.net/search?text=%s&type=track&page=0&nocorrect=false", url.QueryEscape(searchQuery))

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "YandexMusic/2024.01.1 (Android; Google Pixel)")
		if p.token != "" {
			req.Header.Set("Authorization", "OAuth "+p.token)
		}

		resp, err := p.client.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var data yandexSearchResponse
		err = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		if err != nil {
			continue
		}

		tracks := data.Result.Tracks.Results
		if len(tracks) == 0 {
			continue
		}

		// 1. Поиск трека с валидацией названия/артиста и длительности (если задана)
		for _, track := range tracks {
			if !track.LyricsAvailable {
				continue
			}

			trackArtist := ""
			if len(track.Artists) > 0 {
				trackArtist = track.Artists[0].Name
			}

			if !MatchTrack(q, trackArtist, track.Title) {
				continue
			}

			dur := time.Duration(track.DurationMs) * time.Millisecond
			if q.Duration > 0 && track.DurationMs > 0 {
				diff := math.Abs(float64(track.DurationMs)/1000.0 - q.Duration.Seconds())
				if diff > p.tolerance.Seconds() {
					continue
				}
			}

			return cleanTrackID(track.ID), trackArtist, track.Title, dur, nil
		}

		// 2. Если с учетом погрешности длительности не совпало — берем подходящий по названию/артисту
		for _, track := range tracks {
			if !track.LyricsAvailable {
				continue
			}

			trackArtist := ""
			if len(track.Artists) > 0 {
				trackArtist = track.Artists[0].Name
			}

			if MatchTrack(q, trackArtist, track.Title) {
				dur := time.Duration(track.DurationMs) * time.Millisecond
				return cleanTrackID(track.ID), trackArtist, track.Title, dur, nil
			}
		}
	}

	return "", "", "", 0, ErrNotFound
}

func (p *YandexProvider) fetchLyricsEndpointWithAuth(ctx context.Context, trackID, candArtist, candTitle string, candDuration time.Duration) (*Lyrics, error) {
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
	// Заголовки как у эталонной клиентской библиотеки yandex-music-api: подпись выдана ключом Android-приложения,
	// сервер ожидает соответствующий идентификатор клиента.
	req.Header.Set("User-Agent", "Yandex-Music-API")
	req.Header.Set("X-Yandex-Music-Client", "YandexMusicAndroid/24023621")
	req.Header.Set("Authorization", "OAuth "+p.getToken())

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("yandex: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
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
		return nil, httpStatusError("yandex", dlResp.StatusCode)
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
		Artist:       candArtist,
		Title:        candTitle,
		Duration:     candDuration,
		Source:       "yandex",
		CachedAt:     time.Now(),
	}, nil
}

func (p *YandexProvider) fetchSupplementEndpoint(ctx context.Context, trackID, candArtist, candTitle string, candDuration time.Duration) (*Lyrics, error) {
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
		return nil, httpStatusError("yandex", resp.StatusCode)
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

	if IsDefectiveLyrics(plain) {
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
		Artist:       candArtist,
		Title:        candTitle,
		Duration:     candDuration,
		Source:       "yandex",
		CachedAt:     time.Now(),
	}, nil
}

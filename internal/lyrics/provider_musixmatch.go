// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл provider_musixmatch.go реализует адаптер к Musixmatch — глобальному каталогу
// с огромной базой русскоязычных песен и поддержкой синхронизированных субтитров (LRC).
package lyrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	musixmatchAppID = "web-desktop-app-v1.0"
	musixmatchBase  = "https://apic-desktop.musixmatch.com/ws/1.1"
)

type musixmatchTokenResponse struct {
	Message struct {
		Body struct {
			UserToken string `json:"user_token"`
		} `json:"body"`
	} `json:"message"`
}

type musixmatchMacroResponse struct {
	Message struct {
		Body struct {
			MacroCalls struct {
				TrackSubtitlesGet struct {
					Message struct {
						Body struct {
							Subtitle struct {
								SubtitleBody string `json:"subtitle_body"`
							} `json:"subtitle"`
						} `json:"body"`
					} `json:"message"`
				} `json:"track.subtitles.get"`
				TrackLyricsGet struct {
					Message struct {
						Body struct {
							Lyrics struct {
								LyricsBody string `json:"lyrics_body"`
							} `json:"lyrics"`
						} `json:"body"`
					} `json:"message"`
				} `json:"track.lyrics.get"`
			} `json:"macro_calls"`
		} `json:"body"`
	} `json:"message"`
}

// MusixmatchProvider предоставляет доступ к текстам Musixmatch.
type MusixmatchProvider struct {
	client    *http.Client
	mu        sync.RWMutex
	userToken string
	tokenExp  time.Time
}

// NewMusixmatchProvider создает экземпляр провайдера Musixmatch.
func NewMusixmatchProvider(timeout time.Duration) *MusixmatchProvider {
	return &MusixmatchProvider{
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

func (p *MusixmatchProvider) ID() string {
	return "musixmatch"
}

func (p *MusixmatchProvider) Name() string {
	return "Musixmatch"
}

func (p *MusixmatchProvider) Fetch(ctx context.Context, query SearchQuery) (*Lyrics, error) {
	token, err := p.getOrRefreshToken(ctx)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("app_id", musixmatchAppID)
	params.Set("usertoken", token)
	params.Set("format", "json")
	params.Set("q_artist", query.Artist)
	params.Set("q_track", query.Title)
	if query.Duration > 0 {
		params.Set("f_subtitle_length", fmt.Sprintf("%.0f", query.Duration.Seconds()))
	}

	endpoint := fmt.Sprintf("%s/macro.subtitles.get?%s", musixmatchBase, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	req.Header.Set("Cookie", "AWSELBCORS=0; AWSELB=0")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrNotFound
	}

	var data musixmatchMacroResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	calls := data.Message.Body.MacroCalls
	subBody := strings.TrimSpace(calls.TrackSubtitlesGet.Message.Body.Subtitle.SubtitleBody)
	lyrBody := strings.TrimSpace(calls.TrackLyricsGet.Message.Body.Lyrics.LyricsBody)

	// Очищаем стандартное предупреждение Musixmatch об авторских правах
	if idx := strings.Index(lyrBody, "****** This Lyrics is NOT for Commercial use *******"); idx != -1 {
		lyrBody = strings.TrimSpace(lyrBody[:idx])
	}

	if subBody == "" && lyrBody == "" {
		return nil, ErrNotFound
	}

	var kind LyricsKind = KindPlain
	var lines []Line
	var synced string

	if subBody != "" && strings.Contains(subBody, "[") {
		kind = KindLRC
		synced = subBody
		lines = ParseLRC(subBody)
		if lyrBody == "" {
			lyrBody = StripLRCTimestamps(subBody)
		}
	}

	return &Lyrics{
		Kind:         kind,
		PlainLyrics:  lyrBody,
		SyncedLyrics: synced,
		Lines:        lines,
		Source:       "musixmatch",
		CachedAt:     time.Now(),
	}, nil
}

func (p *MusixmatchProvider) getOrRefreshToken(ctx context.Context) (string, error) {
	p.mu.RLock()
	if p.userToken != "" && time.Now().Before(p.tokenExp) {
		token := p.userToken
		p.mu.RUnlock()
		return token, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.userToken != "" && time.Now().Before(p.tokenExp) {
		return p.userToken, nil
	}

	endpoint := fmt.Sprintf("%s/token.get?app_id=%s", musixmatchBase, musixmatchAppID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var data musixmatchTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}

	token := strings.TrimSpace(data.Message.Body.UserToken)
	if token == "" {
		return "", fmt.Errorf("failed to obtain musixmatch token")
	}

	p.userToken = token
	p.tokenExp = time.Now().Add(12 * time.Hour)
	return token, nil
}

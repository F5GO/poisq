// Package lyrics предоставляет систему поиска, ранжирования и сохранения текстов песен (Lyrics)
// по 4 мировым и региональным источникам (LRCLIB, Яндекс Музыка, NetEase, Genius).
//
// Данный файл provider_netease.go реализует адаптер к NetEase Cloud Music (music.163.com API) —
// крупнейшей азиатской базе синхронизированных текстов, идеально подходящей для редкой музыки,
// аниме-саундтреков (OST), инструментальной и европейской электроники.
package lyrics

import (
	"context"
	"encoding/json"
	"github.com/F5GO/poisq/internal/logger"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const neteaseBaseURL = "https://music.163.com/weapi"

type neteaseSearchResult struct {
	Songs []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Duration int64  `json:"duration"` // в миллисекундах (старый формат)
		DT       int64  `json:"dt"`       // в миллисекундах (weapi)
		Artists  []struct {
			Name string `json:"name"`
		} `json:"artists"`
		AR []struct {
			Name string `json:"name"`
		} `json:"ar"`
		Album struct {
			Name string `json:"name"`
		} `json:"album"`
		AL struct {
			Name string `json:"name"`
		} `json:"al"`
	} `json:"songs"`
}

// UnmarshalJSON терпимо разбирает поле result: когда ничего не найдено,
// NetEase иногда возвращает строку или null вместо объекта.
func (r *neteaseSearchResult) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed[0] != '{' {
		*r = neteaseSearchResult{}
		return nil
	}
	type alias neteaseSearchResult
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*r = neteaseSearchResult(a)
	return nil
}

type neteaseSearchResponse struct {
	Result neteaseSearchResult `json:"result"`
}

type neteaseLyricResponse struct {
	Nolyric     bool `json:"nolyric"`
	Uncollected bool `json:"uncollected"`
	Lrc         struct {
		Lyric string `json:"lyric"`
	} `json:"lrc"`
}

// NetEaseProvider осуществляет поиск синхронизированных текстов в базе NetEase Cloud Music (163.com).
type NetEaseProvider struct {
	client    *http.Client
	tolerance time.Duration
}

// NewNetEaseProvider создает новый экземпляр провайдера NetEase Cloud Music.
func NewNetEaseProvider(timeout, tolerance time.Duration) *NetEaseProvider {
	if timeout <= 0 {
		timeout = 6 * time.Second
	}
	if tolerance <= 0 {
		tolerance = 8 * time.Second
	}
	return &NetEaseProvider{
		tolerance: tolerance,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        50,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// ID возвращает идентификатор провайдера.
func (p *NetEaseProvider) ID() string {
	return "netease"
}

// Name возвращает человекочитаемое имя провайдера.
func (p *NetEaseProvider) Name() string {
	return "NetEase Cloud Music (163.com)"
}

// Fetch ищет трек в каталоге NetEase и загружает его LRC/Plain текст.
func (p *NetEaseProvider) Fetch(ctx context.Context, query SearchQuery) (*Lyrics, error) {
	songID, candArtist, candTitle, candAlbum, candDur, err := p.searchSong(ctx, query)
	if err != nil {
		return nil, err
	}

	form, err := neteaseWeapiForm(map[string]any{"id": songID, "tv": -1, "lv": -1, "rv": -1, "kv": -1, "_nmclfl": 1}, "")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, neteaseBaseURL+"/song/lyric", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setNetEaseHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, httpStatusError("netease", resp.StatusCode)
	}

	var lyrResp neteaseLyricResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&lyrResp); err != nil {
		return nil, err
	}

	if lyrResp.Nolyric {
		return &Lyrics{
			Kind:         KindInstrumental,
			Instrumental: true,
			Source:       p.ID(),
			CachedAt:     time.Now(),
			Artist:       candArtist,
			Title:        candTitle,
			Album:        candAlbum,
			Duration:     candDur,
		}, nil
	}

	rawLrc := strings.TrimSpace(lyrResp.Lrc.Lyric)
	if rawLrc == "" {
		return nil, ErrNotFound
	}

	lines := ParseLRC(rawLrc)
	if len(lines) > 0 {
		return &Lyrics{
			Kind:         KindLRC,
			SyncedLyrics: rawLrc,
			PlainLyrics:  StripLRCTimestamps(rawLrc),
			Lines:        lines,
			Source:       p.ID(),
			CachedAt:     time.Now(),
			Artist:       candArtist,
			Title:        candTitle,
			Album:        candAlbum,
			Duration:     candDur,
		}, nil
	}

	return &Lyrics{
		Kind:        KindPlain,
		PlainLyrics: rawLrc,
		Source:      p.ID(),
		CachedAt:    time.Now(),
		Artist:      candArtist,
		Title:       candTitle,
		Album:       candAlbum,
		Duration:    candDur,
	}, nil
}

func (p *NetEaseProvider) searchSong(ctx context.Context, q SearchQuery) (int64, string, string, string, time.Duration, error) {
	form, err := neteaseWeapiForm(map[string]any{
		"s":      strings.TrimSpace(q.Artist + " " + q.Title),
		"type":   1,
		"limit":  8,
		"offset": 0,
		"total":  true,
	}, "")
	if err != nil {
		return 0, "", "", "", 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, neteaseBaseURL+"/cloudsearch/pc", strings.NewReader(form.Encode()))
	if err != nil {
		return 0, "", "", "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setNetEaseHeaders(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, "", "", "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, "", "", "", 0, httpStatusError("netease", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return 0, "", "", "", 0, err
	}
	var data neteaseSearchResponse
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, "", "", "", 0, err
	}
	if len(data.Result.Songs) == 0 {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		logger.Info("[NETEASE] Поиск «%s %s» вернул 0 песен; ответ сервера: %s", q.Artist, q.Title, snippet)
	}

	var bestID int64
	var bestScore float64
	var bestArtist, bestTitle, bestAlbum string
	var bestDur time.Duration

	for _, s := range data.Result.Songs {
		var artists []string
		for _, a := range s.Artists {
			if a.Name != "" {
				artists = append(artists, a.Name)
			}
		}
		for _, a := range s.AR {
			if a.Name != "" {
				artists = append(artists, a.Name)
			}
		}
		candArtist := strings.Join(artists, ", ")
		durMs := s.Duration
		if durMs == 0 {
			durMs = s.DT
		}
		candDur := time.Duration(durMs) * time.Millisecond
		albumName := s.Album.Name
		if albumName == "" {
			albumName = s.AL.Name
		}

		if q.Duration > 0 && candDur > 0 {
			diff := math.Abs(float64(q.Duration - candDur))
			if diff > float64(p.tolerance*2) {
				continue
			}
		}

		score := ScoreCandidate(q, candArtist, s.Name, albumName, candDur, KindLRC, "", p.tolerance)
		if score.Accepted && score.Total > bestScore {
			bestScore = score.Total
			bestID = s.ID
			bestArtist = candArtist
			bestTitle = s.Name
			bestAlbum = albumName
			bestDur = candDur
		}
	}

	if bestID == 0 {
		if n := len(data.Result.Songs); n > 0 {
			first := data.Result.Songs[0]
			logger.Info("[NETEASE] Найдено песен: %d, но ни одна не прошла фильтр; первая: «%s» (%dс) для запроса «%s — %s»",
				n, first.Name, (first.Duration+first.DT)/1000, q.Artist, q.Title)
		}
		return 0, "", "", "", 0, ErrNotFound
	}
	return bestID, bestArtist, bestTitle, bestAlbum, bestDur, nil
}

func setNetEaseHeaders(req *http.Request) {
	req.Header.Set("Referer", "https://music.163.com")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
}

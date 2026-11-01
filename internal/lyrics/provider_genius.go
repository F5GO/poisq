// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл provider_genius.go реализует адаптер к Genius — крупнейшей мировой и русскоязычной базе
// текстов песен с разметкой куплетов, припевов и расширенными метаданными.
package lyrics

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	geniusLyricsContainerRegex = regexp.MustCompile(`(?s)<div[^>]*data-lyrics-container="true"[^>]*>(.*?)</div>`)
	htmlBrRegex                = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlTagRegex               = regexp.MustCompile(`<[^>]+>`)
	geniusContributorsRegex    = regexp.MustCompile(`(?i)^\d+\s*contributors?.*$`)
	geniusEmbedRegex           = regexp.MustCompile(`(?i)\d*\s*embed$`)
	geniusLyricsHeaderRegex    = regexp.MustCompile(`(?i)^.*lyrics$`)
	geniusLikeRegex            = regexp.MustCompile(`(?i)^you might also like.*$`)
)

type geniusMultiSearchResponse struct {
	Response struct {
		Sections []struct {
			Type string `json:"type"`
			Hits []struct {
				Result struct {
					ID            int64  `json:"id"`
					Title         string `json:"title"`
					URL           string `json:"url"`
					PrimaryArtist struct {
						Name string `json:"name"`
					} `json:"primary_artist"`
				} `json:"result"`
			} `json:"hits"`
		} `json:"sections"`
	} `json:"response"`
}

// GeniusProvider осуществляет поиск и извлечение текстов песен с платформы Genius.
type GeniusProvider struct {
	client *http.Client
}

// NewGeniusProvider создает новый экземпляр провайдера Genius.
func NewGeniusProvider(timeout time.Duration) *GeniusProvider {
	return &GeniusProvider{
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

func (p *GeniusProvider) ID() string {
	return "genius"
}

func (p *GeniusProvider) Name() string {
	return "Genius (RU)"
}

func (p *GeniusProvider) Fetch(ctx context.Context, query SearchQuery) (*Lyrics, error) {
	// 1. Поиск страницы песни через публичный эндпоинт Genius Multi Search
	pageURL, candArtist, candTitle, err := p.searchSongURL(ctx, query)
	if err != nil {
		return nil, err
	}

	// 2. Скачивание HTML страницы и извлечение текста песни
	return p.scrapeLyrics(ctx, pageURL, candArtist, candTitle)
}

func (p *GeniusProvider) searchSongURL(ctx context.Context, q SearchQuery) (string, string, string, error) {
	searchQuery := fmt.Sprintf("%s %s", q.Artist, q.Title)
	endpoint := fmt.Sprintf("https://genius.com/api/search/multi?q=%s", url.QueryEscape(searchQuery))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", ErrNotFound
	}

	var data geniusMultiSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", "", err
	}

	normQueryArtist := NormalizeTrackString(q.Artist)
	var fallbackURL, fallbackArtist, fallbackTitle string

	for _, sec := range data.Response.Sections {
		if sec.Type == "song" || sec.Type == "top_hit" {
			for _, hit := range sec.Hits {
				if hit.Result.URL == "" {
					continue
				}
				candArtist := hit.Result.PrimaryArtist.Name
				candTitle := hit.Result.Title

				// Валидируем, что найденный трек совпадает с искомым исполнителем и песней
				if MatchTrack(q, candArtist, candTitle) {
					// Если исполнитель совпадает точно (без посторонних фитов и коллабораций) — сразу отдаем его
					if NormalizeTrackString(candArtist) == normQueryArtist {
						return hit.Result.URL, candArtist, candTitle, nil
					}
					// Иначе запоминаем как запасной вариант (если точного сольного трека не найдется)
					if fallbackURL == "" {
						fallbackURL = hit.Result.URL
						fallbackArtist = candArtist
						fallbackTitle = candTitle
					}
				}
			}
		}
	}

	if fallbackURL != "" {
		return fallbackURL, fallbackArtist, fallbackTitle, nil
	}

	return "", "", "", ErrNotFound
}

func (p *GeniusProvider) scrapeLyrics(ctx context.Context, pageURL, candArtist, candTitle string) (*Lyrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrNotFound
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	htmlContent := string(bodyBytes)
	matches := geniusLyricsContainerRegex.FindAllStringSubmatch(htmlContent, -1)
	if len(matches) == 0 {
		return nil, ErrNotFound
	}

	var sb strings.Builder
	for _, m := range matches {
		if len(m) > 1 {
			chunk := m[1]
			chunk = htmlBrRegex.ReplaceAllString(chunk, "\n")
			chunk = htmlTagRegex.ReplaceAllString(chunk, "")
			sb.WriteString(chunk)
			sb.WriteString("\n")
		}
	}

	plain := cleanGeniusText(html.UnescapeString(sb.String()))
	if plain == "" || IsDefectiveLyrics(plain) {
		return nil, ErrNotFound
	}

	return &Lyrics{
		Kind:        KindPlain,
		PlainLyrics: plain,
		Artist:      candArtist,
		Title:       candTitle,
		Source:      "genius",
		CachedAt:    time.Now(),
	}, nil
}

// cleanGeniusText вычищает служебные артефакты разметки Genius (Contributors, Embed, You might also like).
func cleanGeniusText(raw string) string {
	lines := strings.Split(raw, "\n")
	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			cleaned = append(cleaned, "")
			continue
		}
		if geniusContributorsRegex.MatchString(trimmed) {
			continue
		}
		if geniusLyricsHeaderRegex.MatchString(trimmed) && len(trimmed) < 40 {
			continue
		}
		if geniusLikeRegex.MatchString(trimmed) {
			continue
		}
		if geniusEmbedRegex.MatchString(trimmed) {
			trimmed = geniusEmbedRegex.ReplaceAllString(trimmed, "")
			trimmed = strings.TrimSpace(trimmed)
			if trimmed == "" {
				continue
			}
		}
		cleaned = append(cleaned, trimmed)
	}
	res := strings.Join(cleaned, "\n")
	return strings.TrimSpace(res)
}

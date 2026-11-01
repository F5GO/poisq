// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл provider_amalgama.go реализует адаптер к русскоязычным порталам текстов песен (Text-pesni / Amalgama),
// обеспечивая покрытие русскоязычной эстрады, рока, бардовской песни и переводов зарубежных хитов.
package lyrics

import (
	"context"
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
	textPesniResultLinkRegex = regexp.MustCompile(`(?i)<a[^>]+href="(/pesnya/[^"]+)"[^>]*>([\s\S]*?)</a>`)
	textPesniContentRegex    = regexp.MustCompile(`(?s)<div[^>]*class="(?:text_song|song_text)"[^>]*>(.*?)</div>`)
)

// AmalgamaProvider осуществляет поиск текстов на русскоязычных порталах (Text-pesni).
type AmalgamaProvider struct {
	client *http.Client
}

// NewAmalgamaProvider создает новый экземпляр провайдера Amalgama / Text-pesni.
func NewAmalgamaProvider(timeout time.Duration) *AmalgamaProvider {
	return &AmalgamaProvider{
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

func (p *AmalgamaProvider) ID() string {
	return "amalgama"
}

func (p *AmalgamaProvider) Name() string {
	return "Text-pesni / Amalgama"
}

func (p *AmalgamaProvider) Fetch(ctx context.Context, query SearchQuery) (*Lyrics, error) {
	searchQuery := fmt.Sprintf("%s %s", query.Artist, query.Title)
	searchURL := fmt.Sprintf("https://text-pesni.com/search?q=%s", url.QueryEscape(searchQuery))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")

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

	matches := textPesniResultLinkRegex.FindAllStringSubmatch(string(bodyBytes), -1)
	if len(matches) == 0 {
		return nil, ErrNotFound
	}

	var bestURL string
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		linkURL := m[1]
		linkText := html.UnescapeString(m[2])
		linkText = strings.ReplaceAll(linkText, "Текст песни", "")
		linkText = strings.ReplaceAll(linkText, "слова песни", "")

		// Проверяем совпадение исполнителя и названия в анкоре ссылки
		if MatchArtist(query.Artist, linkText) && MatchTitle(query.Title, linkText) {
			bestURL = fmt.Sprintf("https://text-pesni.com%s", linkURL)
			break
		}
	}

	if bestURL == "" {
		// Запасная проверка: вхождение названия трека в URL ссылки
		normTitle := NormalizeTrackString(query.Title)
		for _, m := range matches {
			linkURL := m[1]
			normURL := NormalizeTrackString(linkURL)
			if strings.Contains(normURL, normTitle) {
				bestURL = fmt.Sprintf("https://text-pesni.com%s", linkURL)
				break
			}
		}
	}

	if bestURL == "" {
		return nil, ErrNotFound
	}

	return p.scrapeSong(ctx, bestURL)
}

func (p *AmalgamaProvider) scrapeSong(ctx context.Context, songURL string) (*Lyrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, songURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")

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
	contentMatches := textPesniContentRegex.FindStringSubmatch(htmlContent)
	if len(contentMatches) < 2 {
		return nil, ErrNotFound
	}

	rawText := contentMatches[1]
	rawText = htmlBrRegex.ReplaceAllString(rawText, "\n")
	rawText = htmlTagRegex.ReplaceAllString(rawText, "")
	cleanText := html.UnescapeString(rawText)
	cleanText = strings.TrimSpace(cleanText)

	if cleanText == "" {
		return nil, ErrNotFound
	}

	return &Lyrics{
		Kind:        KindPlain,
		PlainLyrics: cleanText,
		Source:      "amalgama",
		CachedAt:    time.Now(),
	}, nil
}

// Package lyrics предоставляет систему поиска, ранжирования и сохранения текстов песен (Lyrics).
//
// Данный файл discover_providers.go реализует поиск записей по названию (без артиста) для LRCLIB, Яндекс Музыки
// и NetEase — это источник «реальных написаний» исполнителя для умного поиска (см. discovery.go).
package lyrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Discover ищет в LRCLIB записи по названию трека.
func (p *CustomLrclibProvider) Discover(ctx context.Context, title string, _ time.Duration) ([]DiscoveredTrack, error) {
	params := url.Values{"track_name": []string{title}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/search?%s", p.baseURL, params.Encode()), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "poisq-lyrics/1.0")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpStatusError(p.id, resp.StatusCode)
	}
	var recs []lrclibResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&recs); err != nil {
		return nil, err
	}
	out := make([]DiscoveredTrack, 0, len(recs))
	for _, r := range recs {
		out = append(out, DiscoveredTrack{
			Provider: p.id, Artist: r.ArtistName, Title: r.TrackName,
			Duration: time.Duration(r.Duration * float64(time.Second)),
		})
	}
	return out, nil
}

// Discover ищет в каталоге Яндекс Музыки треки по названию.
func (p *YandexProvider) Discover(ctx context.Context, title string, _ time.Duration) ([]DiscoveredTrack, error) {
	endpoint := fmt.Sprintf("https://api.music.yandex.net/search?text=%s&type=track&page=0&nocorrect=false", url.QueryEscape(title))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "YandexMusic/2024.01.1 (Android; Google Pixel)")
	if tok := p.getToken(); tok != "" {
		req.Header.Set("Authorization", "OAuth "+tok)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpStatusError("yandex", resp.StatusCode)
	}
	var data yandexSearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&data); err != nil {
		return nil, err
	}
	var out []DiscoveredTrack
	for _, t := range data.Result.Tracks.Results {
		if len(t.Artists) == 0 {
			continue
		}
		var names []string
		for _, a := range t.Artists {
			names = append(names, a.Name)
		}
		out = append(out, DiscoveredTrack{
			Provider: "yandex", Artist: strings.Join(names[:1], ""), Title: t.Title,
			Duration: time.Duration(t.DurationMs) * time.Millisecond,
		})
	}
	return out, nil
}

// Discover ищет в NetEase песни по названию (зашифрованный weapi).
func (p *NetEaseProvider) Discover(ctx context.Context, title string, _ time.Duration) ([]DiscoveredTrack, error) {
	form, err := neteaseWeapiForm(map[string]any{"s": title, "type": 1, "limit": 20, "offset": 0, "total": true}, "")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, neteaseBaseURL+"/cloudsearch/pc", strings.NewReader(form.Encode()))
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
	var data neteaseSearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&data); err != nil {
		return nil, err
	}
	var out []DiscoveredTrack
	for _, s := range data.Result.Songs {
		artist := ""
		if len(s.AR) > 0 {
			artist = s.AR[0].Name
		} else if len(s.Artists) > 0 {
			artist = s.Artists[0].Name
		}
		if artist == "" {
			continue
		}
		ms := s.DT
		if ms == 0 {
			ms = s.Duration
		}
		out = append(out, DiscoveredTrack{Provider: "netease", Artist: artist, Title: s.Name, Duration: time.Duration(ms) * time.Millisecond})
	}
	return out, nil
}

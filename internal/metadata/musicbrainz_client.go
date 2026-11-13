// Package metadata отвечает за аудит медиатеки, исправление битой кодировки (CP1251 -> UTF-8 NFC),
// обогащение через MusicBrainz API и поиск обложек.
//
// Данный файл musicbrainz_client.go реализует клиент к официальному API MusicBrainz (ws/2)
// со строгим соблюдением лимита 1 запрос в секунду для получения каноничных имен артистов
// (например, «Максим -> МакSим»), международных псевдонимов (Aliases), года выпуска и MBID релиза.
package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/httpclient"
)

const (
	musicBrainzBaseURL   = "https://musicbrainz.org/ws/2"
	musicBrainzUserAgent = "poisq/0.2.0 ( https://github.com/F5GO/poisq )"
)

// MBArtistInfo содержит каноничное имя артиста, MBID и список международных алиасов.
type MBArtistInfo struct {
	ArtistMBID    string   `json:"artistMbid"`
	CanonicalName string   `json:"canonicalName"`
	SortName      string   `json:"sortName"`
	Country       string   `json:"country"`
	Aliases       []string `json:"aliases"`
	Score         int      `json:"score"`
}

// MBReleaseInfo содержит данные официального альбома/релиза из MusicBrainz.
type MBReleaseInfo struct {
	ReleaseMBID     string `json:"releaseMbid"`
	ArtistMBID      string `json:"artistMbid"`
	CanonicalArtist string `json:"canonicalArtist"`
	AlbumTitle      string `json:"albumTitle"`
	Year            int    `json:"year"`
	TrackCount      int    `json:"trackCount"`
	Score           int    `json:"score"`
}

type MBArtistLinks struct {
	ArtistMBID   string `json:"artistMbid"`
	WikidataID   string `json:"wikidataId"`
	WikipediaURL string `json:"wikipediaUrl"`
}

type mbArtistSearchResp struct {
	Artists []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		SortName string `json:"sort-name"`
		Country  string `json:"country"`
		Score    int    `json:"score"`
		Aliases  []struct {
			Name string `json:"name"`
		} `json:"aliases"`
	} `json:"artists"`
}

type mbReleaseSearchResp struct {
	Releases []struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Score        int    `json:"score"`
		Date         string `json:"date"`
		TrackCount   int    `json:"track-count"`
		ArtistCredit []struct {
			Name   string `json:"name"`
			Artist struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"artist"`
		} `json:"artist-credit"`
	} `json:"releases"`
}

type mbArtistLinksResp struct {
	ID        string `json:"id"`
	Relations []struct {
		Type string `json:"type"`
		URL  struct {
			Resource string `json:"resource"`
		} `json:"url"`
	} `json:"relations"`
}

// MusicBrainzClient управляет запросами к MusicBrainz API с защитой от бана по IP (1 req/sec).
type MusicBrainzClient struct {
	rc      *httpclient.ResilientClient
	baseURL string

	namesMu    sync.Mutex
	namesCache map[string]mbNamesEntry
}

type mbNamesEntry struct {
	names []string
	err   error
	at    time.Time
}

// NewMusicBrainzClient создает новый экземпляр клиента MusicBrainz.
func NewMusicBrainzClient() *MusicBrainzClient {
	return &MusicBrainzClient{
		// Строго 1 параллельный запрос с интервалом 1.1 секунды согласно правилам MusicBrainz
		rc:      httpclient.NewResilientClient(12*time.Second, 1, 1100*time.Millisecond, 3),
		baseURL: musicBrainzBaseURL,
	}
}

// SearchArtists возвращает до восьми реальных кандидатов MusicBrainz для дальнейшей проверки контекстом медиатеки.
func (mb *MusicBrainzClient) SearchArtists(ctx context.Context, artistName string, limit int) ([]MBArtistInfo, error) {
	q := strings.TrimSpace(artistName)
	if q == "" {
		return nil, fmt.Errorf("пустое имя артиста")
	}
	if len(q) > 2<<10 {
		return nil, fmt.Errorf("имя артиста превышает допустимый размер")
	}
	if limit < 1 {
		limit = 3
	}
	if limit > 8 {
		limit = 8
	}

	endpoint := fmt.Sprintf("%s/artist?query=%s&fmt=json&limit=%d",
		mb.baseURL, url.QueryEscape(fmt.Sprintf(`artist:"%s" OR alias:"%s"`, q, q)), limit)

	body, status, _, err := mb.rc.DoWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", musicBrainzUserAgent)
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("MusicBrainz artist search: %w", ctx.Err())
		}
		return nil, fmt.Errorf("MusicBrainz artist search failed")
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("MusicBrainz Artist HTTP %d", status)
	}

	var parsed mbArtistSearchResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("invalid MusicBrainz artist response: %w", err)
	}
	if len(parsed.Artists) > limit {
		parsed.Artists = parsed.Artists[:limit]
	}
	result := make([]MBArtistInfo, 0, len(parsed.Artists))
	for _, artist := range parsed.Artists {
		aliases := make([]string, 0, len(artist.Aliases))
		seen := map[string]bool{strings.ToLower(NormalizeNFC(artist.Name)): true}
		for _, alias := range artist.Aliases {
			cleanAlias := NormalizeNFC(strings.TrimSpace(alias.Name))
			key := strings.ToLower(cleanAlias)
			if cleanAlias != "" && !seen[key] {
				seen[key] = true
				aliases = append(aliases, cleanAlias)
			}
		}
		result = append(result, MBArtistInfo{
			ArtistMBID: artist.ID, CanonicalName: NormalizeNFC(artist.Name),
			SortName: NormalizeNFC(artist.SortName), Country: artist.Country,
			Aliases: aliases, Score: artist.Score,
		})
	}
	return result, nil
}

// LookupArtist ищет каноничное написание имени артиста (например, "Максим" -> "МакSим") и его алиасы.
func (mb *MusicBrainzClient) LookupArtist(ctx context.Context, artistName string) (*MBArtistInfo, error) {
	artists, err := mb.SearchArtists(ctx, artistName, 3)
	if err != nil {
		return nil, err
	}
	if len(artists) == 0 || artists[0].Score < 85 {
		return nil, fmt.Errorf("артист %q не найден с высокой точностью", artistName)
	}
	artist := artists[0]
	return &artist, nil
}

// LookupArtistLinks returns only exact Wikipedia/Wikidata links attached to a MusicBrainz artist ID.
func (mb *MusicBrainzClient) LookupArtistLinks(ctx context.Context, artistMBID string) (*MBArtistLinks, error) {
	artistMBID = strings.TrimSpace(artistMBID)
	if !validMusicBrainzID(artistMBID) {
		return nil, fmt.Errorf("invalid MusicBrainz artist id")
	}
	endpoint := fmt.Sprintf("%s/artist/%s?inc=url-rels&fmt=json", mb.baseURL, url.PathEscape(artistMBID))
	body, status, _, err := mb.rc.DoWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", musicBrainzUserAgent)
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("MusicBrainz artist links request failed")
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("MusicBrainz artist links HTTP %d", status)
	}
	var parsed mbArtistLinksResp
	if err := json.Unmarshal(body, &parsed); err != nil || !strings.EqualFold(parsed.ID, artistMBID) {
		return nil, fmt.Errorf("invalid MusicBrainz artist links response")
	}
	result := &MBArtistLinks{ArtistMBID: artistMBID}
	for _, relation := range parsed.Relations {
		linked, err := url.Parse(relation.URL.Resource)
		if err != nil || linked.Scheme != "https" {
			continue
		}
		switch strings.ToLower(relation.Type) {
		case "wikidata":
			if strings.EqualFold(linked.Host, "www.wikidata.org") {
				id := strings.TrimPrefix(linked.Path, "/wiki/")
				if validWikidataID(id) {
					result.WikidataID = id
				}
			}
		case "wikipedia":
			if strings.HasSuffix(strings.ToLower(linked.Host), ".wikipedia.org") && strings.HasPrefix(linked.Path, "/wiki/") {
				result.WikipediaURL = linked.String()
			}
		}
	}
	return result, nil
}

func validMusicBrainzID(value string) bool {
	if len(value) < 8 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') && r != '-' {
			return false
		}
	}
	return true
}

func validWikidataID(value string) bool {
	if len(value) < 2 || len(value) > 32 || value[0] != 'Q' {
		return false
	}
	for _, r := range value[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// LookupRelease ищет официальный релиз альбома в MusicBrainz для канонизации тегов папки.
func (mb *MusicBrainzClient) LookupRelease(ctx context.Context, artist, album string) (*MBReleaseInfo, error) {
	cleanArtist := strings.TrimSpace(artist)
	cleanAlbum := strings.TrimSpace(album)
	if cleanArtist == "" || cleanAlbum == "" {
		return nil, fmt.Errorf("необходимо указать артиста и альбом")
	}

	lucene := fmt.Sprintf(`artist:"%s" AND release:"%s"`, cleanArtist, cleanAlbum)
	endpoint := fmt.Sprintf("%s/release?query=%s&fmt=json&limit=5",
		mb.baseURL, url.QueryEscape(lucene))

	body, status, _, err := mb.rc.DoWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", musicBrainzUserAgent)
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("MusicBrainz Release HTTP %d", status)
	}

	var parsed mbReleaseSearchResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Releases) == 0 || parsed.Releases[0].Score < 85 {
		return nil, fmt.Errorf("релиз %q - %q не найден в MusicBrainz", artist, album)
	}

	top := parsed.Releases[0]
	canonicalArtist := cleanArtist
	artistMBID := ""
	if len(top.ArtistCredit) > 0 {
		if top.ArtistCredit[0].Artist.Name != "" {
			canonicalArtist = top.ArtistCredit[0].Artist.Name
		} else if top.ArtistCredit[0].Name != "" {
			canonicalArtist = top.ArtistCredit[0].Name
		}
		artistMBID = top.ArtistCredit[0].Artist.ID
	}

	year := 0
	if len(top.Date) >= 4 {
		if y, err := strconv.Atoi(top.Date[:4]); err == nil && y > 1900 && y < 2100 {
			year = y
		}
	}

	return &MBReleaseInfo{
		ReleaseMBID:     top.ID,
		ArtistMBID:      artistMBID,
		CanonicalArtist: NormalizeNFC(canonicalArtist),
		AlbumTitle:      NormalizeNFC(top.Title),
		Year:            year,
		TrackCount:      top.TrackCount,
		Score:           top.Score,
	}, nil
}

// ArtistNames возвращает каноничное имя артиста и его реальные алиасы из MusicBrainz
// (реализует lyrics.ArtistAliasSource). Запросы идут через общий лимитер 1 req/sec.
func (mb *MusicBrainzClient) ArtistNames(ctx context.Context, name string) ([]string, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	mb.namesMu.Lock()
	if e, ok := mb.namesCache[key]; ok && time.Since(e.at) < 24*time.Hour {
		mb.namesMu.Unlock()
		return e.names, e.err
	}
	mb.namesMu.Unlock()

	info, err := mb.LookupArtist(ctx, name)
	var names []string
	if err == nil {
		names = append([]string{info.CanonicalName}, info.Aliases...)
	}
	if ctx.Err() == nil { // не кэшируем отмену по таймауту
		mb.namesMu.Lock()
		if mb.namesCache == nil {
			mb.namesCache = make(map[string]mbNamesEntry)
		}
		mb.namesCache[key] = mbNamesEntry{names: names, err: err, at: time.Now()}
		mb.namesMu.Unlock()
	}
	return names, err
}

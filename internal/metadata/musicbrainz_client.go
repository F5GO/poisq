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
	ReleaseMBID   string `json:"releaseMbid"`
	ArtistMBID    string `json:"artistMbid"`
	CanonicalArtist string `json:"canonicalArtist"`
	AlbumTitle    string `json:"albumTitle"`
	Year          int    `json:"year"`
	TrackCount    int    `json:"trackCount"`
	Score         int    `json:"score"`
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
		ID          string `json:"id"`
		Title       string `json:"title"`
		Score       int    `json:"score"`
		Date        string `json:"date"`
		TrackCount  int    `json:"track-count"`
		ArtistCredit []struct {
			Name   string `json:"name"`
			Artist struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"artist"`
		} `json:"artist-credit"`
	} `json:"releases"`
}

// MusicBrainzClient управляет запросами к MusicBrainz API с защитой от бана по IP (1 req/sec).
type MusicBrainzClient struct {
	rc *httpclient.ResilientClient

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
		rc: httpclient.NewResilientClient(12*time.Second, 1, 1100*time.Millisecond, 3),
	}
}

// LookupArtist ищет каноничное написание имени артиста (например, "Максим" -> "МакSим") и его алиасы.
func (mb *MusicBrainzClient) LookupArtist(ctx context.Context, artistName string) (*MBArtistInfo, error) {
	q := strings.TrimSpace(artistName)
	if q == "" {
		return nil, fmt.Errorf("пустое имя артиста")
	}

	endpoint := fmt.Sprintf("%s/artist?query=%s&fmt=json&limit=3",
		musicBrainzBaseURL, url.QueryEscape(fmt.Sprintf(`artist:"%s" OR alias:"%s"`, q, q)))

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
		return nil, fmt.Errorf("MusicBrainz Artist HTTP %d", status)
	}

	var parsed mbArtistSearchResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Artists) == 0 || parsed.Artists[0].Score < 85 {
		return nil, fmt.Errorf("артист %q не найден с высокой точностью", artistName)
	}

	top := parsed.Artists[0]
	var aliases []string
	seen := map[string]bool{strings.ToLower(top.Name): true}
	for _, al := range top.Aliases {
		cleanAl := NormalizeNFC(strings.TrimSpace(al.Name))
		if cleanAl != "" && !seen[strings.ToLower(cleanAl)] {
			seen[strings.ToLower(cleanAl)] = true
			aliases = append(aliases, cleanAl)
		}
	}

	return &MBArtistInfo{
		ArtistMBID:    top.ID,
		CanonicalName: NormalizeNFC(top.Name),
		SortName:      NormalizeNFC(top.SortName),
		Country:       top.Country,
		Aliases:       aliases,
		Score:         top.Score,
	}, nil
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
		musicBrainzBaseURL, url.QueryEscape(lucene))

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

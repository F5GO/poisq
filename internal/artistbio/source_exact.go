package artistbio

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/metadata"
)

const biographyUserAgent = "poisq/0.2.0 (artist biography enrichment)"

type artistLinksLookup interface {
	LookupArtistLinks(ctx context.Context, artistMBID string) (*metadata.MBArtistLinks, error)
}

type biographyKeyPool interface {
	Acquire(provider string) (*keypool.LeasedKey, error)
	ReportSuccess(keyID int64)
	ReportRateLimit429(keyID int64, details string)
	ReportUnavailable503(keyID int64, details string)
	ReportInvalid(keyID int64, httpCode int, details string)
}

func NewDefaultBiographySources(links artistLinksLookup, keys biographyKeyPool) ([]BiographySource, error) {
	constructors := []func() (BiographySource, error){
		func() (BiographySource, error) { return NewWikipediaSource(links) },
		func() (BiographySource, error) { return NewTheAudioDBSource() },
		func() (BiographySource, error) { return NewLastFMSource(keys) },
		func() (BiographySource, error) { return NewYandexBiographySource(keys) },
		func() (BiographySource, error) { return NewDeezerBiographySource() },
		func() (BiographySource, error) { return NewAppleMusicBiographySource(keys) },
		func() (BiographySource, error) { return NewDiscogsBiographySource(keys) },
	}
	sources := make([]BiographySource, 0, len(constructors))
	for _, construct := range constructors {
		source, err := construct()
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

type WikipediaSource struct {
	links     artistLinksLookup
	wikidata  *providerHTTPClient
	wikipedia *providerHTTPClient
}

func NewWikipediaSource(links artistLinksLookup) (*WikipediaSource, error) {
	return newWikipediaSource(links, "https://www.wikidata.org", "https://ru.wikipedia.org")
}

func newWikipediaSource(links artistLinksLookup, wikidataBase, wikipediaBase string) (*WikipediaSource, error) {
	if links == nil {
		return nil, fmt.Errorf("wikipedia source requires artist links")
	}
	wd, err := newProviderHTTPClient(wikidataBase, biographyUserAgent, 100*time.Millisecond)
	if err != nil {
		return nil, err
	}
	wp, err := newProviderHTTPClient(wikipediaBase, biographyUserAgent, 100*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &WikipediaSource{links: links, wikidata: wd, wikipedia: wp}, nil
}

func (*WikipediaSource) Provider() Provider { return ProviderWikipedia }

func (s *WikipediaSource) Fetch(ctx context.Context, artist ResolvedArtist) (*BiographySourceResult, error) {
	mbid := strings.TrimSpace(artist.Identity.MusicBrainzID)
	if mbid == "" {
		return nil, nil
	}
	links, err := s.links.LookupArtistLinks(ctx, mbid)
	if err != nil {
		return nil, fmt.Errorf("wikipedia identity links unavailable")
	}
	title := russianWikipediaTitle(links.WikipediaURL)
	sourceID := ""
	if title == "" && links.WikidataID != "" {
		var entity struct {
			Entities map[string]struct {
				Sitelinks map[string]struct {
					Title string `json:"title"`
				} `json:"sitelinks"`
			} `json:"entities"`
		}
		if _, err := s.wikidata.getJSON(ctx, "/wiki/Special:EntityData/"+links.WikidataID+".json", nil, nil, &entity); err != nil {
			return nil, fmt.Errorf("wikidata artist link unavailable")
		}
		title = entity.Entities[links.WikidataID].Sitelinks["ruwiki"].Title
		sourceID = links.WikidataID
	}
	if strings.TrimSpace(title) == "" || len(title) > MaxIdentityNameBytes {
		return nil, nil
	}
	query := url.Values{"action": {"query"}, "prop": {"extracts"}, "exintro": {"1"}, "explaintext": {"1"}, "redirects": {"1"}, "format": {"json"}, "titles": {title}}
	var response struct {
		Query struct {
			Pages map[string]struct {
				PageID  int64  `json:"pageid"`
				Title   string `json:"title"`
				Extract string `json:"extract"`
			} `json:"pages"`
		} `json:"query"`
	}
	if _, err := s.wikipedia.getJSON(ctx, "/w/api.php", query, nil, &response); err != nil {
		return nil, fmt.Errorf("wikipedia summary unavailable")
	}
	pageIDs := make([]string, 0, len(response.Query.Pages))
	for id := range response.Query.Pages {
		pageIDs = append(pageIDs, id)
	}
	sort.Strings(pageIDs)
	for _, id := range pageIDs {
		page := response.Query.Pages[id]
		text := strings.TrimSpace(page.Extract)
		if page.PageID <= 0 || text == "" {
			continue
		}
		if sourceID == "" {
			sourceID = "page:" + strconv.FormatInt(page.PageID, 10)
		}
		return &BiographySourceResult{
			SourceArtistID: sourceID, SourceURL: "https://ru.wikipedia.org/wiki/" + url.PathEscape(page.Title),
			OriginalText: text, LanguageHint: "ru", MatchConfidence: 1,
			EvidenceCodes: []string{"id:musicbrainz", "link:wikipedia"},
		}, nil
	}
	return nil, nil
}

func russianWikipediaTitle(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Host, "ru.wikipedia.org") || !strings.HasPrefix(parsed.Path, "/wiki/") {
		return ""
	}
	title, err := url.PathUnescape(strings.TrimPrefix(parsed.Path, "/wiki/"))
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(title, "_", " ")
}

type TheAudioDBSource struct{ http *providerHTTPClient }

func NewTheAudioDBSource() (*TheAudioDBSource, error) {
	return newTheAudioDBSource("https://www.theaudiodb.com/api/v1/json/123")
}

func newTheAudioDBSource(base string) (*TheAudioDBSource, error) {
	client, err := newProviderHTTPClient(base, biographyUserAgent, 200*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &TheAudioDBSource{http: client}, nil
}

func (*TheAudioDBSource) Provider() Provider { return ProviderTheAudioDB }

func (s *TheAudioDBSource) Fetch(ctx context.Context, artist ResolvedArtist) (*BiographySourceResult, error) {
	mbid := strings.TrimSpace(artist.Identity.MusicBrainzID)
	if mbid == "" {
		return nil, nil
	}
	var response struct {
		Artists []struct {
			ID          string `json:"idArtist"`
			MBID        string `json:"strMusicBrainzID"`
			BiographyRU string `json:"strBiographyRU"`
			BiographyEN string `json:"strBiographyEN"`
		} `json:"artists"`
	}
	if _, err := s.http.getJSON(ctx, "/artist-mb.php", url.Values{"i": {mbid}}, nil, &response); err != nil {
		return nil, fmt.Errorf("theaudiodb artist unavailable")
	}
	for _, item := range response.Artists {
		if !strings.EqualFold(strings.TrimSpace(item.MBID), mbid) {
			continue
		}
		text, language := strings.TrimSpace(item.BiographyRU), "ru"
		if text == "" {
			text, language = strings.TrimSpace(item.BiographyEN), "en"
		}
		if text == "" {
			return nil, nil
		}
		return &BiographySourceResult{
			SourceArtistID: item.ID, SourceURL: "https://www.theaudiodb.com/artist/" + url.PathEscape(item.ID),
			OriginalText: text, LanguageHint: language, MatchConfidence: 1,
			EvidenceCodes: []string{"id:musicbrainz"},
		}, nil
	}
	return nil, nil
}

type LastFMSource struct {
	http *providerHTTPClient
	keys biographyKeyPool
}

func NewLastFMSource(keys biographyKeyPool) (*LastFMSource, error) {
	return newLastFMSource("https://ws.audioscrobbler.com", keys)
}

func newLastFMSource(base string, keys biographyKeyPool) (*LastFMSource, error) {
	client, err := newProviderHTTPClient(base, biographyUserAgent, 250*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &LastFMSource{http: client, keys: keys}, nil
}

func (*LastFMSource) Provider() Provider { return ProviderLastFM }

func (s *LastFMSource) Fetch(ctx context.Context, artist ResolvedArtist) (*BiographySourceResult, error) {
	if s.keys == nil || artist.Identity.MusicBrainzID == "" {
		return nil, nil
	}
	leased, err := s.keys.Acquire("lastfm")
	if err != nil || leased == nil {
		return nil, nil
	}
	query := url.Values{
		"method": {"artist.getinfo"}, "mbid": {artist.Identity.MusicBrainzID}, "lang": {"ru"},
		"autocorrect": {"0"}, "format": {"json"}, "api_key": {leased.PlainKey},
	}
	var response struct {
		Artist struct {
			Name string `json:"name"`
			MBID string `json:"mbid"`
			Bio  struct {
				Summary string `json:"summary"`
				Content string `json:"content"`
			} `json:"bio"`
		} `json:"artist"`
	}
	status, err := s.http.getJSON(ctx, "/2.0/", query, nil, &response)
	if err != nil {
		s.reportKeyFailure(leased.ID, status)
		return nil, fmt.Errorf("lastfm artist unavailable")
	}
	s.keys.ReportSuccess(leased.ID)
	if !strings.EqualFold(strings.TrimSpace(response.Artist.MBID), artist.Identity.MusicBrainzID) {
		return nil, nil
	}
	text := cleanProviderText(response.Artist.Bio.Content)
	if text == "" {
		text = cleanProviderText(response.Artist.Bio.Summary)
	}
	if text == "" {
		return nil, nil
	}
	return &BiographySourceResult{
		SourceArtistID: artist.Identity.MusicBrainzID,
		SourceURL:      "https://www.last.fm/music/" + url.PathEscape(response.Artist.Name),
		OriginalText:   text, LanguageHint: "ru", MatchConfidence: 1,
		EvidenceCodes: []string{"id:musicbrainz"},
	}, nil
}

func (s *LastFMSource) reportKeyFailure(id int64, status int) {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		s.keys.ReportInvalid(id, status, "Last.fm authorization failed")
	case http.StatusTooManyRequests:
		s.keys.ReportRateLimit429(id, "Last.fm rate limit")
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		s.keys.ReportUnavailable503(id, "Last.fm unavailable")
	}
}

var providerTags = regexp.MustCompile(`(?s)<[^>]*>`)

func cleanProviderText(value string) string {
	value = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n").Replace(value)
	value = providerTags.ReplaceAllString(value, "")
	value = html.UnescapeString(value)
	lines := strings.Split(value, "\n")
	output := lines[:0]
	for _, line := range lines {
		if clean := strings.Join(strings.Fields(line), " "); clean != "" {
			output = append(output, clean)
		}
	}
	return strings.TrimSpace(strings.Join(output, "\n"))
}

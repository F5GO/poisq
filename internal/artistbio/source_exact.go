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
	"unicode"
	"unicode/utf8"

	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/metadata"
)

const biographyUserAgent = "poisq/0.2.0 (artist biography enrichment)"

type artistLinksLookup interface {
	LookupArtistLinks(ctx context.Context, artistMBID string) (*metadata.MBArtistLinks, error)
}

type regionalWikipediaLinkSource interface {
	ExactWikipediaLink(ctx context.Context, artist ResolvedArtist) (rawURL, sourceID string, err error)
}

type biographyKeyPool interface {
	Acquire(provider string) (*keypool.LeasedKey, error)
	ReportSuccess(keyID int64)
	ReportRateLimit429(keyID int64, details string)
	ReportUnavailable503(keyID int64, details string)
	ReportInvalid(keyID int64, httpCode int, details string)
}

func NewDefaultBiographySources(links artistLinksLookup, keys biographyKeyPool) ([]BiographySource, error) {
	yandex, err := NewYandexBiographySource(keys)
	if err != nil {
		return nil, err
	}
	wikipedia, err := NewWikipediaSource(links)
	if err != nil {
		return nil, err
	}
	wikipedia.regionalLinks = yandex
	constructors := []func() (BiographySource, error){
		func() (BiographySource, error) { return wikipedia, nil },
		func() (BiographySource, error) { return NewTheAudioDBSource() },
		func() (BiographySource, error) { return NewLastFMSource(keys) },
		func() (BiographySource, error) { return yandex, nil },
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
	links         artistLinksLookup
	regionalLinks regionalWikipediaLinkSource
	wikidata      *providerHTTPClient
	wikipedias    map[string]*providerHTTPClient
}

func NewWikipediaSource(links artistLinksLookup) (*WikipediaSource, error) {
	return newWikipediaSourceWithLanguages(links, "https://www.wikidata.org", map[string]string{
		"ru": "https://ru.wikipedia.org", "en": "https://en.wikipedia.org", "de": "https://de.wikipedia.org",
		"fr": "https://fr.wikipedia.org", "es": "https://es.wikipedia.org", "it": "https://it.wikipedia.org",
		"pl": "https://pl.wikipedia.org", "uk": "https://uk.wikipedia.org",
	})
}

func newWikipediaSource(links artistLinksLookup, wikidataBase, wikipediaBase string) (*WikipediaSource, error) {
	bases := make(map[string]string, len(wikipediaLanguagePriority))
	for _, language := range wikipediaLanguagePriority {
		bases[language] = wikipediaBase
	}
	return newWikipediaSourceWithLanguages(links, wikidataBase, bases)
}

var wikipediaLanguagePriority = []string{"ru", "en", "de", "fr", "es", "it", "pl", "uk"}

func newWikipediaSourceWithLanguages(links artistLinksLookup, wikidataBase string, wikipediaBases map[string]string) (*WikipediaSource, error) {
	if links == nil {
		return nil, fmt.Errorf("wikipedia source requires artist links")
	}
	wd, err := newProviderHTTPClient(wikidataBase, biographyUserAgent, 100*time.Millisecond)
	if err != nil {
		return nil, err
	}
	wikipedias := make(map[string]*providerHTTPClient, len(wikipediaLanguagePriority))
	for _, language := range wikipediaLanguagePriority {
		base := strings.TrimSpace(wikipediaBases[language])
		if base == "" {
			continue
		}
		client, err := newProviderHTTPClient(base, biographyUserAgent, 100*time.Millisecond)
		if err != nil {
			return nil, err
		}
		wikipedias[language] = client
	}
	if wikipedias["ru"] == nil {
		return nil, fmt.Errorf("wikipedia source requires Russian endpoint")
	}
	return &WikipediaSource{links: links, wikidata: wd, wikipedias: wikipedias}, nil
}

func (*WikipediaSource) Provider() Provider { return ProviderWikipedia }

func (s *WikipediaSource) Fetch(ctx context.Context, artist ResolvedArtist) (*BiographySourceResult, error) {
	mbid := strings.TrimSpace(artist.Identity.MusicBrainzID)
	links := &metadata.MBArtistLinks{}
	if mbid != "" {
		var err error
		links, err = s.links.LookupArtistLinks(ctx, mbid)
		if err != nil {
			return nil, fmt.Errorf("wikipedia identity links unavailable")
		}
	}
	article, linkedArticle := exactWikipediaArticle(links.WikipediaURL)
	sourceID := ""
	evidenceCodes := []string{"id:musicbrainz", "link:wikipedia"}
	if (article.Language != "ru" || article.Title == "") && links.WikidataID != "" {
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
		for _, language := range wikipediaLanguagePriority {
			title := strings.TrimSpace(entity.Entities[links.WikidataID].Sitelinks[language+"wiki"].Title)
			if title != "" && s.wikipedias[language] != nil {
				article = wikipediaArticle{Language: language, Title: title}
				break
			}
		}
		sourceID = links.WikidataID
	}
	if article.Title == "" && mbid != "" {
		result, err := s.fetchExactRussianReleaseMatch(ctx, artist)
		if err != nil {
			return nil, fmt.Errorf("wikipedia exact artist fallback unavailable")
		}
		if result != nil {
			return result, nil
		}
	}
	if article.Title == "" && s.regionalLinks != nil {
		rawURL, regionalID, err := s.regionalLinks.ExactWikipediaLink(ctx, artist)
		if err != nil {
			return nil, fmt.Errorf("regional wikipedia identity link unavailable")
		}
		if linked, ok := exactWikipediaArticle(rawURL); ok {
			article, linkedArticle, sourceID = linked, true, regionalID
			evidenceCodes = []string{"catalog:yandex", "link:wikipedia"}
		}
	}
	if article.Title == "" && linkedArticle {
		article, _ = exactWikipediaArticle(links.WikipediaURL)
	}
	if strings.TrimSpace(article.Title) == "" || len(article.Title) > MaxIdentityNameBytes || s.wikipedias[article.Language] == nil {
		return nil, nil
	}
	query := url.Values{"action": {"query"}, "prop": {"extracts"}, "exintro": {"1"}, "explaintext": {"1"}, "redirects": {"1"}, "format": {"json"}, "titles": {article.Title}}
	var response struct {
		Query struct {
			Pages map[string]struct {
				PageID  int64  `json:"pageid"`
				Title   string `json:"title"`
				Extract string `json:"extract"`
			} `json:"pages"`
		} `json:"query"`
	}
	if _, err := s.wikipedias[article.Language].getJSON(ctx, "/w/api.php", query, nil, &response); err != nil {
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
			SourceArtistID: sourceID, SourceURL: "https://" + article.Language + ".wikipedia.org/wiki/" + url.PathEscape(page.Title),
			OriginalText: text, LanguageHint: article.Language, MatchConfidence: 1,
			EvidenceCodes: evidenceCodes,
		}, nil
	}
	return nil, nil
}

// fetchExactRussianReleaseMatch covers recently created Wikipedia articles
// whose Wikidata/MusicBrainz relations have not propagated yet. It never does
// a fuzzy search: the exact artist title must resolve to a page whose intro
// mentions a real album, or at least two tracks, from the local library. The
// already resolved catalog identity plus this independent release evidence
// prevents a same-name person or disambiguation page from being accepted.
func (s *WikipediaSource) fetchExactRussianReleaseMatch(ctx context.Context, artist ResolvedArtist) (*BiographySourceResult, error) {
	if s.wikipedias["ru"] == nil || (len(artist.Context.Albums) == 0 && len(artist.Context.Tracks) == 0) {
		return nil, nil
	}
	query := url.Values{
		"action": {"query"}, "prop": {"extracts|pageprops"}, "exintro": {"1"}, "explaintext": {"1"},
		"ppprop": {"wikibase_item"}, "redirects": {"1"}, "format": {"json"}, "titles": {artist.Context.Name},
	}
	var response struct {
		Query struct {
			Pages map[string]struct {
				PageID    int64  `json:"pageid"`
				Title     string `json:"title"`
				Extract   string `json:"extract"`
				PageProps struct {
					WikibaseItem string `json:"wikibase_item"`
				} `json:"pageprops"`
			} `json:"pages"`
		} `json:"query"`
	}
	if _, err := s.wikipedias["ru"].getJSON(ctx, "/w/api.php", query, nil, &response); err != nil {
		return nil, err
	}
	pageIDs := make([]string, 0, len(response.Query.Pages))
	for id := range response.Query.Pages {
		pageIDs = append(pageIDs, id)
	}
	sort.Strings(pageIDs)
	for _, id := range pageIDs {
		page := response.Query.Pages[id]
		text := strings.TrimSpace(page.Extract)
		if page.PageID <= 0 || text == "" || !wikipediaTitleMatchesArtist(page.Title, artist.Context.Name) {
			continue
		}
		evidence := wikipediaReleaseEvidence(text, artist.Context.Albums, artist.Context.Tracks)
		if evidence == "" {
			continue
		}
		sourceID := strings.TrimSpace(page.PageProps.WikibaseItem)
		if sourceID == "" {
			sourceID = "page:" + strconv.FormatInt(page.PageID, 10)
		}
		return &BiographySourceResult{
			SourceArtistID: sourceID, SourceURL: "https://ru.wikipedia.org/wiki/" + url.PathEscape(page.Title),
			OriginalText: text, LanguageHint: "ru", MatchConfidence: 1,
			EvidenceCodes: []string{"id:musicbrainz", "name:exact", evidence},
		}, nil
	}
	return nil, nil
}

func wikipediaTitleMatchesArtist(title, artist string) bool {
	if identityNamesEquivalent(title, artist) {
		return true
	}
	if suffix := strings.Index(title, " ("); suffix > 0 {
		return identityNamesEquivalent(title[:suffix], artist)
	}
	return false
}

func wikipediaReleaseEvidence(extract string, albums []IdentityRelease, tracks []string) string {
	haystack := " " + searchableBiographyText(extract) + " "
	for _, album := range albums {
		if needle := searchableReleaseTitle(album.Title); needle != "" && strings.Contains(haystack, " "+needle+" ") {
			return "album:mentioned"
		}
	}
	matches := 0
	seen := make(map[string]struct{})
	for _, track := range tracks {
		needle := searchableReleaseTitle(track)
		if needle == "" {
			continue
		}
		if _, exists := seen[needle]; exists {
			continue
		}
		seen[needle] = struct{}{}
		if strings.Contains(haystack, " "+needle+" ") {
			matches++
			if matches >= 2 {
				return "track:multiple_mentioned"
			}
		}
	}
	return ""
}

func searchableReleaseTitle(value string) string {
	normalized := searchableBiographyText(value)
	if utf8.RuneCountInString(strings.ReplaceAll(normalized, " ", "")) < 6 {
		return ""
	}
	return normalized
}

func searchableBiographyText(value string) string {
	var output strings.Builder
	space := true
	for _, r := range strings.ToLower(metadata.NormalizeNFC(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			output.WriteRune(r)
			space = false
		} else if !space {
			output.WriteByte(' ')
			space = true
		}
	}
	return strings.Join(strings.Fields(output.String()), " ")
}

type wikipediaArticle struct {
	Language string
	Title    string
}

func exactWikipediaArticle(raw string) (wikipediaArticle, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !strings.HasPrefix(parsed.Path, "/wiki/") {
		return wikipediaArticle{}, false
	}
	hostParts := strings.Split(strings.ToLower(parsed.Hostname()), ".")
	if len(hostParts) != 3 || hostParts[1] != "wikipedia" || hostParts[2] != "org" || len(hostParts[0]) < 2 || len(hostParts[0]) > 12 {
		return wikipediaArticle{}, false
	}
	title, err := url.PathUnescape(strings.TrimPrefix(parsed.Path, "/wiki/"))
	if err != nil {
		return wikipediaArticle{}, false
	}
	return wikipediaArticle{Language: hostParts[0], Title: strings.ReplaceAll(title, "_", " ")}, true
}

func russianWikipediaTitle(raw string) string {
	article, ok := exactWikipediaArticle(raw)
	if !ok || article.Language != "ru" {
		return ""
	}
	return article.Title
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

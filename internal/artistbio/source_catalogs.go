package artistbio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/keypool"
)

type AppleMusicBiographySource struct {
	http *providerHTTPClient
	keys biographyKeyPool
}

func NewAppleMusicBiographySource(keys biographyKeyPool) (*AppleMusicBiographySource, error) {
	return newAppleMusicBiographySource("https://api.music.apple.com", keys)
}

func newAppleMusicBiographySource(base string, keys biographyKeyPool) (*AppleMusicBiographySource, error) {
	client, err := newProviderHTTPClient(base, biographyUserAgent, 200*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &AppleMusicBiographySource{http: client, keys: keys}, nil
}

func (*AppleMusicBiographySource) Provider() Provider { return ProviderAppleMusic }

type appleArtistResource struct {
	ID         string `json:"id"`
	Attributes struct {
		Name           string `json:"name"`
		URL            string `json:"url"`
		EditorialNotes struct {
			Standard string `json:"standard"`
			Short    string `json:"short"`
		} `json:"editorialNotes"`
	} `json:"attributes"`
}

func (s *AppleMusicBiographySource) Fetch(ctx context.Context, artist ResolvedArtist) (*BiographySourceResult, error) {
	leased, ok := acquireBiographyKey(s.keys, "apple_music")
	if !ok {
		return nil, nil
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+leased.PlainKey)
	var search struct {
		Results struct {
			Artists struct {
				Data []appleArtistResource `json:"data"`
			} `json:"artists"`
		} `json:"results"`
	}
	status, err := s.http.getJSON(ctx, "/v1/catalog/ru/search", url.Values{
		"term": {artist.Context.Name}, "types": {"artists"}, "limit": {"3"},
	}, headers, &search)
	if err != nil {
		reportBiographyKeyFailure(s.keys, leased.ID, status, "Apple Music")
		return nil, fmt.Errorf("apple music artist search unavailable")
	}
	if len(search.Results.Artists.Data) > 3 {
		search.Results.Artists.Data = search.Results.Artists.Data[:3]
	}
	candidates := make([]IdentityCandidate, 0, len(search.Results.Artists.Data))
	for _, found := range search.Results.Artists.Data {
		if !safeCatalogID(found.ID) {
			continue
		}
		var albums struct {
			Data []struct {
				Attributes struct {
					Name        string `json:"name"`
					ReleaseDate string `json:"releaseDate"`
				} `json:"attributes"`
			} `json:"data"`
		}
		status, err = s.http.getJSON(ctx, "/v1/catalog/ru/artists/"+found.ID+"/albums", url.Values{"limit": {"30"}}, headers, &albums)
		if err != nil {
			continue
		}
		candidate := IdentityCandidate{ID: "apple:" + found.ID, Provider: ProviderAppleMusic, ProviderArtistID: found.ID, Name: found.Attributes.Name}
		for i, album := range albums.Data {
			if i == MaxIdentityAlbums {
				break
			}
			year := 0
			if len(album.Attributes.ReleaseDate) >= 4 {
				year, _ = strconv.Atoi(album.Attributes.ReleaseDate[:4])
			}
			candidate.Albums = append(candidate.Albums, IdentityRelease{Title: album.Attributes.Name, Year: year})
		}
		candidates = append(candidates, candidate)
	}
	resolution := ResolveIdentityDeterministically(artist.Context, candidates)
	if resolution.Status != ResolutionResolved || resolution.Provider != ProviderAppleMusic {
		return nil, nil
	}
	var details struct {
		Data []appleArtistResource `json:"data"`
	}
	status, err = s.http.getJSON(ctx, "/v1/catalog/ru/artists/"+resolution.ProviderArtistID, nil, headers, &details)
	if err != nil {
		reportBiographyKeyFailure(s.keys, leased.ID, status, "Apple Music")
		return nil, fmt.Errorf("apple music artist details unavailable")
	}
	s.keys.ReportSuccess(leased.ID)
	if len(details.Data) != 1 || details.Data[0].ID != resolution.ProviderArtistID {
		return nil, nil
	}
	resource := details.Data[0]
	text := cleanProviderText(resource.Attributes.EditorialNotes.Standard)
	if text == "" {
		text = cleanProviderText(resource.Attributes.EditorialNotes.Short)
	}
	if text == "" {
		return nil, nil
	}
	sourceURL := resource.Attributes.URL
	if !safeHTTPSHost(sourceURL, "music.apple.com") {
		sourceURL = "https://music.apple.com/ru/artist/-/" + resource.ID
	}
	return &BiographySourceResult{
		SourceArtistID: resource.ID, SourceURL: sourceURL, OriginalText: text,
		LanguageHint: "ru", MatchConfidence: resolution.Confidence,
		EvidenceCodes: append([]string(nil), resolution.EvidenceCodes...),
	}, nil
}

type DiscogsBiographySource struct {
	http *providerHTTPClient
	keys biographyKeyPool
}

func NewDiscogsBiographySource(keys biographyKeyPool) (*DiscogsBiographySource, error) {
	return newDiscogsBiographySource("https://api.discogs.com", keys)
}

func newDiscogsBiographySource(base string, keys biographyKeyPool) (*DiscogsBiographySource, error) {
	client, err := newProviderHTTPClient(base, biographyUserAgent, time.Second)
	if err != nil {
		return nil, err
	}
	return &DiscogsBiographySource{http: client, keys: keys}, nil
}

func (*DiscogsBiographySource) Provider() Provider { return ProviderDiscogs }

func (s *DiscogsBiographySource) Fetch(ctx context.Context, artist ResolvedArtist) (*BiographySourceResult, error) {
	leased, ok := acquireBiographyKey(s.keys, "discogs")
	if !ok {
		return nil, nil
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Discogs token="+leased.PlainKey)
	var search struct {
		Results []struct {
			ID    json.RawMessage `json:"id"`
			Title string          `json:"title"`
		} `json:"results"`
	}
	status, err := s.http.getJSON(ctx, "/database/search", url.Values{
		"q": {artist.Context.Name}, "type": {"artist"}, "per_page": {"3"},
	}, headers, &search)
	if err != nil {
		reportBiographyKeyFailure(s.keys, leased.ID, status, "Discogs")
		return nil, fmt.Errorf("discogs artist search unavailable")
	}
	if len(search.Results) > 3 {
		search.Results = search.Results[:3]
	}
	type discogsDetails struct {
		ID      json.RawMessage `json:"id"`
		Name    string          `json:"name"`
		Profile string          `json:"profile"`
	}
	detailsByID := make(map[string]discogsDetails, len(search.Results))
	candidates := make([]IdentityCandidate, 0, len(search.Results))
	for _, found := range search.Results {
		id := boundedNumericJSONID(found.ID)
		if id == "" {
			continue
		}
		var details discogsDetails
		if _, err := s.http.getJSON(ctx, "/artists/"+id, nil, headers, &details); err != nil || boundedNumericJSONID(details.ID) != id {
			continue
		}
		detailsByID[id] = details
		var releases struct {
			Releases []struct {
				Title string `json:"title"`
				Year  int    `json:"year"`
			} `json:"releases"`
		}
		if _, err := s.http.getJSON(ctx, "/artists/"+id+"/releases", url.Values{"per_page": {"30"}, "sort": {"year"}, "sort_order": {"desc"}}, headers, &releases); err != nil {
			continue
		}
		candidate := IdentityCandidate{ID: "discogs:" + id, Provider: ProviderDiscogs, ProviderArtistID: id, Name: details.Name}
		for i, release := range releases.Releases {
			if i == MaxIdentityAlbums {
				break
			}
			candidate.Albums = append(candidate.Albums, IdentityRelease{Title: release.Title, Year: release.Year})
		}
		candidates = append(candidates, candidate)
	}
	resolution := ResolveIdentityDeterministically(artist.Context, candidates)
	if resolution.Status != ResolutionResolved || resolution.Provider != ProviderDiscogs {
		return nil, nil
	}
	details := detailsByID[resolution.ProviderArtistID]
	text := cleanDiscogsText(details.Profile)
	if text == "" {
		return nil, nil
	}
	s.keys.ReportSuccess(leased.ID)
	return &BiographySourceResult{
		SourceArtistID: resolution.ProviderArtistID,
		SourceURL:      "https://www.discogs.com/artist/" + resolution.ProviderArtistID,
		OriginalText:   text, MatchConfidence: resolution.Confidence,
		EvidenceCodes: append([]string(nil), resolution.EvidenceCodes...),
	}, nil
}

var discogsMarkup = regexp.MustCompile(`(?i)\[/?(?:b|i|u)\]|\[/?a(?:=[^\]]*)?\]`)

func cleanDiscogsText(value string) string {
	return cleanProviderText(discogsMarkup.ReplaceAllString(value, ""))
}

func acquireBiographyKey(keys biographyKeyPool, provider string) (*keypool.LeasedKey, bool) {
	if keys == nil {
		return nil, false
	}
	leased, err := keys.Acquire(provider)
	return leased, err == nil && leased != nil && strings.TrimSpace(leased.PlainKey) != ""
}

func reportBiographyKeyFailure(keys biographyKeyPool, id int64, status int, provider string) {
	if keys == nil {
		return
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		keys.ReportInvalid(id, status, provider+" authorization failed")
	case http.StatusTooManyRequests:
		keys.ReportRateLimit429(id, provider+" rate limit")
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		keys.ReportUnavailable503(id, provider+" unavailable")
	}
}

func safeCatalogID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func safeHTTPSHost(raw, host string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && strings.EqualFold(parsed.Host, host) && parsed.User == nil
}

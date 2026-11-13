package artistbio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	deezerBiographyContainer = `id="naboo_artist_biography_resume_premium"`
	deezerDescriptionSpan    = `<span itemprop="description">`
	maxDeezerDescriptionHTML = MaxBiographyBytes * 2
)

type DeezerBiographySource struct {
	api *providerHTTPClient
	web *providerHTTPClient
}

func NewDeezerBiographySource() (*DeezerBiographySource, error) {
	return newDeezerBiographySource("https://api.deezer.com", "https://www.deezer.com")
}

func newDeezerBiographySource(apiBase, webBase string) (*DeezerBiographySource, error) {
	api, err := newProviderHTTPClient(apiBase, biographyUserAgent, 120*time.Millisecond)
	if err != nil {
		return nil, err
	}
	web, err := newProviderHTTPClient(webBase, "Mozilla/5.0 (compatible; poisq/0.2; artist biography)", 250*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &DeezerBiographySource{api: api, web: web}, nil
}

func (*DeezerBiographySource) Provider() Provider { return ProviderDeezer }

func (s *DeezerBiographySource) Fetch(ctx context.Context, artist ResolvedArtist) (*BiographySourceResult, error) {
	var search struct {
		Data []struct {
			ID   json.RawMessage `json:"id"`
			Name string          `json:"name"`
		} `json:"data"`
	}
	if _, err := s.api.getJSON(ctx, "/search/artist", url.Values{"q": {artist.Context.Name}, "limit": {"3"}}, nil, &search); err != nil {
		return nil, fmt.Errorf("deezer artist search unavailable")
	}
	if len(search.Data) > 3 {
		search.Data = search.Data[:3]
	}
	candidates := make([]IdentityCandidate, 0, len(search.Data))
	for _, found := range search.Data {
		id := boundedNumericJSONID(found.ID)
		if id == "" {
			continue
		}
		var details struct {
			ID   json.RawMessage `json:"id"`
			Name string          `json:"name"`
		}
		if _, err := s.api.getJSON(ctx, "/artist/"+id, nil, nil, &details); err != nil || boundedNumericJSONID(details.ID) != id {
			continue
		}
		candidate := IdentityCandidate{
			ID: "deezer:" + id, Provider: ProviderDeezer,
			ProviderArtistID: id, Name: details.Name,
		}
		var albums struct {
			Data []struct {
				Title       string `json:"title"`
				ReleaseDate string `json:"release_date"`
			} `json:"data"`
		}
		if _, err := s.api.getJSON(ctx, "/artist/"+id+"/albums", url.Values{"limit": {"30"}}, nil, &albums); err != nil {
			continue
		}
		if len(albums.Data) > MaxIdentityAlbums {
			albums.Data = albums.Data[:MaxIdentityAlbums]
		}
		for _, album := range albums.Data {
			year := 0
			if len(album.ReleaseDate) >= 4 {
				year, _ = strconv.Atoi(album.ReleaseDate[:4])
			}
			candidate.Albums = append(candidate.Albums, IdentityRelease{Title: album.Title, Year: year})
		}
		candidates = append(candidates, candidate)
	}
	resolution := ResolveIdentityDeterministically(artist.Context, candidates)
	if resolution.Status != ResolutionResolved || resolution.Provider != ProviderDeezer || resolution.Confidence < AutoResolveConfidence {
		return nil, nil
	}
	id := resolution.ProviderArtistID
	headers := make(http.Header)
	headers.Set("Accept-Language", "ru-RU,ru;q=0.9")
	body, status, err := s.web.get(ctx, "/ru/artist/"+id, nil, headers)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("deezer artist biography unavailable")
	}
	text, err := extractDeezerDescription(body)
	if err != nil {
		return nil, nil
	}
	return &BiographySourceResult{
		SourceArtistID: id, SourceURL: "https://www.deezer.com/ru/artist/" + id,
		OriginalText: text, LanguageHint: "ru", MatchConfidence: resolution.Confidence,
		EvidenceCodes: append([]string(nil), resolution.EvidenceCodes...),
	}, nil
}

func extractDeezerDescription(body []byte) (string, error) {
	page := string(body)
	containerIndex := strings.Index(page, deezerBiographyContainer)
	if containerIndex < 0 {
		return "", fmt.Errorf("deezer biography container is missing")
	}
	remainder := page[containerIndex+len(deezerBiographyContainer):]
	spanIndex := strings.Index(remainder, deezerDescriptionSpan)
	if spanIndex < 0 || spanIndex > 4<<10 {
		return "", fmt.Errorf("deezer biography description is missing")
	}
	remainder = remainder[spanIndex+len(deezerDescriptionSpan):]
	end := strings.Index(strings.ToLower(remainder), "</span>")
	if end < 0 || end > maxDeezerDescriptionHTML {
		return "", fmt.Errorf("deezer biography description exceeds bounds")
	}
	fragment := remainder[:end]
	lower := strings.ToLower(fragment)
	if strings.Contains(lower, "<script") || strings.Contains(lower, "<style") || strings.Contains(lower, "<iframe") {
		return "", fmt.Errorf("deezer biography contains unsafe markup")
	}
	text := cleanProviderText(fragment)
	if text == "" || len(text) > MaxBiographyBytes {
		return "", fmt.Errorf("deezer biography text is empty or oversized")
	}
	return text, nil
}

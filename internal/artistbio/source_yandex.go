package artistbio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const yandexBiographyUserAgent = "YandexMusic/2024.01.1 (Android; Google Pixel)"

type YandexBiographySource struct {
	http *providerHTTPClient
	keys biographyKeyPool
}

func NewYandexBiographySource(keys biographyKeyPool) (*YandexBiographySource, error) {
	return newYandexBiographySource("https://api.music.yandex.net", keys)
}

func newYandexBiographySource(base string, keys biographyKeyPool) (*YandexBiographySource, error) {
	client, err := newProviderHTTPClient(base, yandexBiographyUserAgent, 200*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &YandexBiographySource{http: client, keys: keys}, nil
}

func (*YandexBiographySource) Provider() Provider { return ProviderYandexMusic }

func (s *YandexBiographySource) Fetch(ctx context.Context, artist ResolvedArtist) (*BiographySourceResult, error) {
	var search struct {
		Result struct {
			Artists struct {
				Results []struct {
					ID   json.RawMessage `json:"id"`
					Name string          `json:"name"`
				} `json:"results"`
			} `json:"artists"`
		} `json:"result"`
	}
	query := url.Values{"text": {artist.Context.Name}, "type": {"artist"}, "page": {"0"}, "nocorrect": {"false"}}
	if _, err := s.http.getJSON(ctx, "/search", query, nil, &search); err != nil {
		return nil, fmt.Errorf("yandex artist search unavailable")
	}
	if len(search.Result.Artists.Results) > 3 {
		search.Result.Artists.Results = search.Result.Artists.Results[:3]
	}
	candidates := make([]IdentityCandidate, 0, len(search.Result.Artists.Results))
	for _, found := range search.Result.Artists.Results {
		id := boundedNumericJSONID(found.ID)
		if id == "" || strings.TrimSpace(found.Name) == "" {
			continue
		}
		candidate := IdentityCandidate{
			ID: "yandex:" + id, Provider: ProviderYandexMusic,
			ProviderArtistID: id, Name: found.Name,
		}
		var albums struct {
			Result struct {
				Albums []struct {
					Title string `json:"title"`
					Year  int    `json:"year"`
				} `json:"albums"`
			} `json:"result"`
		}
		albumQuery := url.Values{"page": {"0"}, "page-size": {"30"}, "sort-by": {"year"}}
		if _, err := s.http.getJSON(ctx, "/artists/"+id+"/direct-albums", albumQuery, nil, &albums); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if len(albums.Result.Albums) > MaxIdentityAlbums {
			albums.Result.Albums = albums.Result.Albums[:MaxIdentityAlbums]
		}
		for _, album := range albums.Result.Albums {
			candidate.Albums = append(candidate.Albums, IdentityRelease{Title: album.Title, Year: album.Year})
		}
		candidates = append(candidates, candidate)
	}
	resolution := ResolveIdentityDeterministically(artist.Context, candidates)
	if resolution.Status != ResolutionResolved || resolution.Provider != ProviderYandexMusic || resolution.Confidence < AutoResolveConfidence {
		return nil, nil
	}
	return s.fetchDescription(ctx, resolution.ProviderArtistID, resolution.Confidence, resolution.EvidenceCodes)
}

func (s *YandexBiographySource) fetchDescription(ctx context.Context, id string, confidence float64, evidence []string) (*BiographySourceResult, error) {
	type briefResponse struct {
		Result struct {
			Artist struct {
				ID          json.RawMessage `json:"id"`
				Description struct {
					Text string `json:"text"`
				} `json:"description"`
			} `json:"artist"`
		} `json:"result"`
	}
	var response briefResponse
	status, err := s.http.getJSON(ctx, "/artists/"+id+"/brief-info", nil, nil, &response)
	if err != nil && (status == http.StatusUnauthorized || status == http.StatusForbidden) && s.keys != nil {
		leased, keyErr := s.keys.Acquire("yandex")
		if keyErr == nil && leased != nil {
			headers := make(http.Header)
			headers.Set("Authorization", "OAuth "+leased.PlainKey)
			status, err = s.http.getJSON(ctx, "/artists/"+id+"/brief-info", nil, headers, &response)
			if err == nil {
				s.keys.ReportSuccess(leased.ID)
			} else if status == http.StatusUnauthorized || status == http.StatusForbidden {
				s.keys.ReportInvalid(leased.ID, status, "Yandex authorization failed")
			} else if status == http.StatusTooManyRequests {
				s.keys.ReportRateLimit429(leased.ID, "Yandex rate limit")
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("yandex artist description unavailable")
	}
	if boundedNumericJSONID(response.Result.Artist.ID) != id {
		return nil, nil
	}
	text := strings.TrimSpace(response.Result.Artist.Description.Text)
	if text == "" {
		return nil, nil
	}
	return &BiographySourceResult{
		SourceArtistID: id, SourceURL: "https://music.yandex.ru/artist/" + id,
		OriginalText: text, LanguageHint: "ru", MatchConfidence: confidence,
		EvidenceCodes: append([]string(nil), evidence...),
	}, nil
}

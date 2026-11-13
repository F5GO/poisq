package artistbio

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/F5GO/poisq/internal/httpclient"
)

const (
	tavilyKeyProvider      = "tavily"
	maxSearchIdentityBytes = 2 << 10
	maxSearchResponseBytes = 256 << 10
	maxSearchResults       = 6
	maxSearchSnippetRunes  = 500
)

type ArtistSearchQuery struct {
	ArtistID      string
	Name          string
	Aliases       []string
	Country       string
	MusicBrainzID string
	ProviderIDs   []string
	Albums        []string
	Tracks        []string
}

type WebSearchResult struct {
	ID      string
	Title   string
	URL     string
	Domain  string
	Snippet string
	Score   float64
}

type WebSearchReport struct {
	QueryVersion string
	Results      []WebSearchResult
}

type TavilySearch struct {
	endpoint *url.URL
	client   *httpclient.ResilientClient
	keys     biographyKeyPool
}

func (s *Store) PutWebSearchCache(ctx context.Context, artistID, queryVersion string, results []WebSearchResult, expiresAt time.Time) error {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return err
	}
	if err := validateRequired("search query version", queryVersion, maxVersionBytes); err != nil {
		return err
	}
	if len(results) > maxSearchResults || expiresAt.IsZero() {
		return fmt.Errorf("invalid web search cache")
	}
	for _, result := range results {
		canonical, domain, ok := safePublicEvidenceURL(result.URL)
		if !ok || canonical != result.URL || domain != result.Domain || len(result.Title) > 512 || utf8.RuneCountInString(result.Snippet) > maxSearchSnippetRunes {
			return fmt.Errorf("invalid web search result")
		}
	}
	encoded, err := json.Marshal(results)
	if err != nil || len(encoded) > MaxEvidenceBytes {
		return fmt.Errorf("invalid web search cache")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO artist_bio_web_cache
		(artist_id, query_version, results_json, expires_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(artist_id, query_version) DO UPDATE SET results_json = excluded.results_json,
			expires_at = excluded.expires_at, updated_at = excluded.updated_at`,
		artistID, queryVersion, string(encoded), expiresAt.Unix(), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("store web search cache: %w", err)
	}
	return nil
}

func (s *Store) WebSearchCache(ctx context.Context, artistID, queryVersion string, now time.Time) (WebSearchReport, bool, error) {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return WebSearchReport{}, false, err
	}
	if err := validateRequired("search query version", queryVersion, maxVersionBytes); err != nil {
		return WebSearchReport{}, false, err
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT results_json FROM artist_bio_web_cache
		WHERE artist_id = ? AND query_version = ? AND expires_at > ?`, artistID, queryVersion, now.Unix()).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return WebSearchReport{}, false, nil
	}
	if err != nil {
		return WebSearchReport{}, false, fmt.Errorf("read web search cache: %w", err)
	}
	var results []WebSearchResult
	if len(raw) > MaxEvidenceBytes || json.Unmarshal([]byte(raw), &results) != nil || len(results) > maxSearchResults {
		return WebSearchReport{}, false, fmt.Errorf("invalid stored web search cache")
	}
	for _, result := range results {
		canonical, domain, ok := safePublicEvidenceURL(result.URL)
		if !ok || canonical != result.URL || domain != result.Domain || utf8.RuneCountInString(result.Snippet) > maxSearchSnippetRunes {
			return WebSearchReport{}, false, fmt.Errorf("invalid stored web search result")
		}
	}
	return WebSearchReport{QueryVersion: queryVersion, Results: results}, true, nil
}

func NewTavilySearch(keys biographyKeyPool) (*TavilySearch, error) {
	return newTavilySearch("https://api.tavily.com", keys)
}

func newTavilySearch(baseURL string, keys biographyKeyPool) (*TavilySearch, error) {
	if keys == nil {
		return nil, fmt.Errorf("tavily search requires key pool")
	}
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("invalid tavily base url")
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/search"
	client := httpclient.NewResilientClient(12*time.Second, 1, time.Second, 2)
	if err := client.RestrictOrigin(base.Scheme + "://" + base.Host); err != nil {
		return nil, err
	}
	if err := client.SetMaxResponseBytes(maxSearchResponseBytes); err != nil {
		return nil, err
	}
	return &TavilySearch{endpoint: &endpoint, client: client, keys: keys}, nil
}

func (s *TavilySearch) Search(ctx context.Context, input ArtistSearchQuery) (WebSearchReport, error) {
	query, version, err := buildArtistSearchQuery(input)
	if err != nil {
		return WebSearchReport{}, err
	}
	leased, err := s.keys.Acquire(tavilyKeyProvider)
	if err != nil || leased == nil || strings.TrimSpace(leased.PlainKey) == "" {
		return WebSearchReport{}, fmt.Errorf("tavily key is unavailable")
	}
	payload, err := json.Marshal(struct {
		APIKey            string `json:"api_key"`
		Query             string `json:"query"`
		SearchDepth       string `json:"search_depth"`
		MaxResults        int    `json:"max_results"`
		IncludeAnswer     bool   `json:"include_answer"`
		IncludeRawContent bool   `json:"include_raw_content"`
	}{leased.PlainKey, query, "basic", maxSearchResults, false, false})
	if err != nil {
		return WebSearchReport{}, fmt.Errorf("encode tavily request")
	}
	body, status, headers, requestErr := s.client.DoWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint.String(), bytes.NewReader(payload))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
		}
		return req, err
	})
	if requestErr != nil {
		if ctx.Err() != nil {
			return WebSearchReport{}, ctx.Err()
		}
		reportSearchKeyFailure(s.keys, leased.ID, status, headers)
		return WebSearchReport{}, fmt.Errorf("tavily request failed")
	}
	if status < 200 || status >= 300 {
		reportSearchKeyFailure(s.keys, leased.ID, status, headers)
		return WebSearchReport{}, fmt.Errorf("tavily returned HTTP %d", status)
	}
	var response struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return WebSearchReport{}, fmt.Errorf("tavily returned malformed json")
	}
	results := make([]WebSearchResult, 0, min(maxSearchResults, len(response.Results)))
	seen := make(map[string]struct{}, maxSearchResults)
	for index, result := range response.Results {
		if len(results) >= maxSearchResults {
			break
		}
		canonical, domain, ok := safePublicEvidenceURL(result.URL)
		if !ok || lowQualityEvidenceURL(canonical, domain) {
			continue
		}
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		title := strings.TrimSpace(result.Title)
		if title == "" || len(title) > 512 {
			continue
		}
		snippet := truncateRunes(strings.Join(strings.Fields(result.Content), " "), maxSearchSnippetRunes)
		if utf8.RuneCountInString(snippet) < 40 {
			continue
		}
		seen[canonical] = struct{}{}
		results = append(results, WebSearchResult{
			ID: fmt.Sprintf("tavily:%d", index), Title: title, URL: canonical,
			Domain: domain, Snippet: snippet, Score: clamp01(result.Score),
		})
	}
	s.keys.ReportSuccess(leased.ID)
	return WebSearchReport{QueryVersion: version, Results: results}, nil
}

func buildArtistSearchQuery(input ArtistSearchQuery) (string, string, error) {
	if err := validateRequired("artist id", input.ArtistID, MaxProviderIDBytes); err != nil {
		return "", "", err
	}
	if err := validateRequired("artist name", input.Name, maxArtistNameBytes); err != nil {
		return "", "", err
	}
	parts := []string{strings.TrimSpace(input.Name), "music artist biography interview official"}
	appendBounded := func(values []string, limit int) {
		for _, value := range values[:min(limit, len(values))] {
			value = strings.Join(strings.Fields(value), " ")
			if value != "" && len(value) <= 256 {
				parts = append(parts, value)
			}
		}
	}
	appendBounded(input.Aliases, 5)
	appendBounded(input.ProviderIDs, 6)
	appendBounded(input.Albums, 3)
	appendBounded(input.Tracks, 5)
	if value := strings.TrimSpace(input.Country); value != "" && len(value) <= 128 {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(input.MusicBrainzID); value != "" && len(value) <= MaxProviderIDBytes {
		parts = append(parts, "musicbrainz "+value)
	}
	sort.Strings(parts[2:])
	query := strings.Join(parts, " ")
	if len(query) > maxSearchIdentityBytes {
		query = truncateUTF8Bytes(query, maxSearchIdentityBytes)
	}
	version, err := evidenceVersion([]EvidenceItem{{ID: input.ArtistID, Origin: query}})
	if err != nil {
		return "", "", err
	}
	return query, version, nil
}

func safePublicEvidenceURL(raw string) (string, string, bool) {
	if len(raw) == 0 || len(raw) > MaxSourceURLBytes {
		return "", "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return "", "", false
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return "", "", false
	}
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String(), host, true
}

func lowQualityEvidenceURL(canonical, domain string) bool {
	lower := strings.ToLower(canonical)
	blockedDomains := []string{"reddit.com", "facebook.com", "instagram.com", "tiktok.com", "x.com", "twitter.com"}
	for _, blocked := range blockedDomains {
		if domain == blocked || strings.HasSuffix(domain, "."+blocked) {
			return true
		}
	}
	for _, marker := range []string{"/lyrics/", "/tickets/", "/forum/", "/forums/"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func reportSearchKeyFailure(keys biographyKeyPool, keyID int64, status int, headers http.Header) {
	details := ""
	if headers != nil {
		details = truncateUTF8Bytes(headers.Get("Retry-After"), 64)
	}
	switch {
	case status == http.StatusTooManyRequests:
		keys.ReportRateLimit429(keyID, details)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		keys.ReportInvalid(keyID, status, "Tavily authorization rejected")
	case status >= 500 || status == 0:
		keys.ReportUnavailable503(keyID, "Tavily unavailable")
	}
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit]))
}

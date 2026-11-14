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
	"unicode"
	"unicode/utf8"

	"github.com/F5GO/poisq/internal/httpclient"
)

const (
	tavilyKeyProvider      = "tavily"
	maxSearchIdentityBytes = 2 << 10
	maxSearchResponseBytes = 256 << 10
	tavilyFetchResults     = 10
	maxSearchResults       = 8
	maxSearchSnippetRunes  = 800
)

type ArtistSearchQuery struct {
	ArtistID           string
	Name               string
	Aliases            []string
	Country            string
	PreferRussian      bool
	IncludeReleaseHint bool
	MusicBrainzID      string
	ProviderIDs        []string
	Albums             []string
	Tracks             []string
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

type safeDiscoveryError struct {
	reason string
	cause  error
}

func (e *safeDiscoveryError) Error() string      { return e.reason }
func (e *safeDiscoveryError) Unwrap() error      { return e.cause }
func (e *safeDiscoveryError) SafeReason() string { return e.reason }

func discoveryError(reason string, cause error) error {
	return &safeDiscoveryError{reason: reason, cause: cause}
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
		return WebSearchReport{}, discoveryError("tavily_key_unavailable", err)
	}
	results, status, headers, err := s.searchTavilyOnce(ctx, leased.PlainKey, query, "basic", "tavily", 0)
	if err != nil {
		if ctx.Err() != nil {
			return WebSearchReport{}, discoveryError("tavily_timeout", ctx.Err())
		}
		reportSearchKeyFailure(s.keys, leased.ID, status, headers)
		return WebSearchReport{}, err
	}
	if releaseQuery := buildArtistReleaseQuery(input); releaseQuery != "" && releaseQuery != query && ctx.Err() == nil {
		relResults, _, _, relErr := s.searchTavilyOnce(ctx, leased.PlainKey, releaseQuery, "basic", "tavily-rel", 0)
		if relErr == nil && len(relResults) > 0 {
			results = mergeSearchResults(results, relResults, maxSearchResults)
		}
	}
	if !hasEnoughSearchResults(results) && ctx.Err() == nil {
		advResults, _, _, advErr := s.searchTavilyOnce(ctx, leased.PlainKey, query, "advanced", "tavily-adv", 3)
		if advErr == nil && len(advResults) > 0 {
			results = mergeSearchResults(advResults, results, maxSearchResults)
		}
	}
	s.keys.ReportSuccess(leased.ID)
	return WebSearchReport{QueryVersion: version, Results: results}, nil
}

func (s *TavilySearch) searchTavilyOnce(ctx context.Context, apiKey, query, depth, prefix string, chunksPerSource int) ([]WebSearchResult, int, http.Header, error) {
	payload, err := json.Marshal(struct {
		Query             string `json:"query"`
		SearchDepth       string `json:"search_depth"`
		ChunksPerSource   int    `json:"chunks_per_source,omitempty"`
		MaxResults        int    `json:"max_results"`
		IncludeAnswer     bool   `json:"include_answer"`
		IncludeRawContent bool   `json:"include_raw_content"`
	}{query, depth, chunksPerSource, tavilyFetchResults, false, false})
	if err != nil {
		return nil, 0, nil, discoveryError("tavily_encode_failed", err)
	}
	body, status, headers, requestErr := s.client.DoWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint.String(), bytes.NewReader(payload))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		return req, err
	})
	if requestErr != nil {
		return nil, status, headers, discoveryError("tavily_transport", requestErr)
	}
	if status < 200 || status >= 300 {
		return nil, status, headers, discoveryError(fmt.Sprintf("tavily_http_%d", status), nil)
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
		return nil, status, headers, discoveryError("tavily_malformed_response", err)
	}
	results := make([]WebSearchResult, 0, min(maxSearchResults, len(response.Results)))
	seen := make(map[string]struct{}, maxSearchResults)
	if prefix == "" {
		prefix = "tavily"
		if depth == "advanced" {
			prefix = "tavily-adv"
		}
	}
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
			ID: fmt.Sprintf("%s:%d", prefix, index), Title: title, URL: canonical,
			Domain: domain, Snippet: snippet, Score: clamp01(result.Score),
		})
	}
	return results, status, headers, nil
}

func hasEnoughSearchResults(results []WebSearchResult) bool {
	if len(results) < 2 {
		return false
	}
	domains := make(map[string]struct{}, len(results))
	totalRunes := 0
	for _, r := range results {
		domains[r.Domain] = struct{}{}
		totalRunes += utf8.RuneCountInString(r.Snippet)
	}
	return len(domains) >= 2 && totalRunes >= 800
}

func mergeSearchResults(primary, fallback []WebSearchResult, limit int) []WebSearchResult {
	out := make([]WebSearchResult, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, list := range [][]WebSearchResult{primary, fallback} {
		for _, item := range list {
			if len(out) >= limit {
				return out
			}
			if _, ok := seen[item.URL]; ok {
				continue
			}
			seen[item.URL] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

func isRussianSearchArtist(input ArtistSearchQuery) bool {
	if input.PreferRussian {
		return true
	}
	switch strings.ToUpper(strings.TrimSpace(input.Country)) {
	case "RU", "BY", "KZ", "UA", "RUSSIA", "РОССИЯ":
		return true
	}
	for _, r := range input.Name {
		if unicode.In(r, unicode.Cyrillic) {
			return true
		}
	}
	for _, alias := range input.Aliases {
		for _, r := range alias {
			if unicode.In(r, unicode.Cyrillic) {
				return true
			}
		}
	}
	return false
}

func buildArtistSearchQuery(input ArtistSearchQuery) (string, string, error) {
	if err := validateRequired("artist id", input.ArtistID, MaxProviderIDBytes); err != nil {
		return "", "", err
	}
	if err := validateRequired("artist name", input.Name, maxArtistNameBytes); err != nil {
		return "", "", err
	}
	name := strings.Join(strings.Fields(input.Name), " ")
	suffix := "music artist biography interview"
	if isRussianSearchArtist(input) {
		suffix = "музыкант биография интервью"
	}
	parts := []string{name, suffix}
	var extra []string
	appendBounded := func(values []string, limit int) {
		for _, value := range values[:min(limit, len(values))] {
			value = strings.Join(strings.Fields(value), " ")
			if value != "" && len(value) <= 256 {
				extra = append(extra, value)
			}
		}
	}
	if len(input.Aliases) > 0 {
		aliases := append([]string(nil), input.Aliases...)
		sort.Strings(aliases)
		appendBounded(aliases, 1)
	}
	if utf8.RuneCountInString(name) <= 5 {
		appendBounded(input.Albums, 1)
		if len(input.Albums) == 0 {
			appendBounded(input.Tracks, 1)
		}
	}
	sort.Strings(extra)
	parts = append(parts, extra...)
	query := strings.Join(parts, " ")
	if len(query) > maxSearchIdentityBytes {
		query = truncateUTF8Bytes(query, maxSearchIdentityBytes)
	}
	releaseHint := buildArtistReleaseQuery(input)
	version, err := evidenceVersion([]EvidenceItem{{ID: input.ArtistID, Origin: "v2:" + query + "|" + releaseHint}})
	if err != nil {
		return "", "", err
	}
	return query, version, nil
}

func buildArtistReleaseQuery(input ArtistSearchQuery) string {
	if !input.IncludeReleaseHint {
		return ""
	}
	name := strings.Join(strings.Fields(input.Name), " ")
	if name == "" {
		return ""
	}
	hint := ""
	if len(input.Albums) > 0 {
		hint = strings.Join(strings.Fields(input.Albums[0]), " ")
	}
	if hint == "" && len(input.Tracks) > 0 {
		hint = strings.Join(strings.Fields(input.Tracks[0]), " ")
	}
	if hint == "" || len(hint) > 256 {
		return ""
	}
	suffix := "music artist"
	if isRussianSearchArtist(input) {
		suffix = "музыкант"
	}
	query := strings.Join([]string{name, hint, suffix}, " ")
	if len(query) > maxSearchIdentityBytes {
		query = truncateUTF8Bytes(query, maxSearchIdentityBytes)
	}
	return query
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
	blockedDomains := []string{"reddit.com", "facebook.com", "instagram.com", "tiktok.com", "x.com", "twitter.com", "linkedin.com", "hh.ru", "habr.com"}
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

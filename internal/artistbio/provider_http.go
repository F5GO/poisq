package artistbio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/httpclient"
)

func boundedNumericJSONID(raw json.RawMessage) string {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if value == "" || len(value) > 32 {
		return ""
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return value
}

type providerHTTPClient struct {
	base      *url.URL
	client    *httpclient.ResilientClient
	userAgent string
}

func newProviderHTTPClient(baseURL, userAgent string, minInterval time.Duration) (*providerHTTPClient, error) {
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return nil, fmt.Errorf("invalid provider base url")
	}
	client := httpclient.NewResilientClient(12*time.Second, 2, minInterval, 3)
	origin := base.Scheme + "://" + base.Host
	if err := client.RestrictOrigin(origin); err != nil {
		return nil, err
	}
	return &providerHTTPClient{base: base, client: client, userAgent: userAgent}, nil
}

func (c *providerHTTPClient) getJSON(ctx context.Context, path string, query url.Values, headers http.Header, target any) (int, error) {
	body, status, err := c.get(ctx, path, query, headers)
	if err != nil {
		return status, err
	}
	if status < 200 || status >= 300 {
		return status, fmt.Errorf("provider returned HTTP %d", status)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return status, fmt.Errorf("provider returned malformed json")
	}
	return status, nil
}

func (c *providerHTTPClient) get(ctx context.Context, path string, query url.Values, headers http.Header) ([]byte, int, error) {
	if c == nil || c.base == nil || c.client == nil {
		return nil, 0, fmt.Errorf("provider client is not configured")
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.ContainsAny(path, "\\?#") {
		return nil, 0, fmt.Errorf("invalid provider path")
	}
	endpoint := *c.base
	endpoint.Path = strings.TrimRight(c.base.Path, "/") + path
	endpoint.RawQuery = query.Encode()
	body, status, _, err := c.client.DoWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, err
		}
		if c.userAgent != "" {
			req.Header.Set("User-Agent", c.userAgent)
		}
		for name, values := range headers {
			for _, value := range values {
				req.Header.Add(name, value)
			}
		}
		return req, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, status, ctx.Err()
		}
		return nil, status, fmt.Errorf("provider request failed")
	}
	return body, status, nil
}

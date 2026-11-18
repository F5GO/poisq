package proxy

// Данный файл cast_auth.go проверяет Subsonic-авторизацию для собственных эндпоинтов poisq (трансляция):
// параметры u/t/s, p или apiKey из запроса отправляются в Navidrome на /rest/ping. Navidrome — единственный источник правды о пользователях.
// Успешные проверки кешируются на 5 минут, чтобы опрос состояния не дёргал Navidrome каждый раз.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const navAuthTTL = 5 * time.Minute

type navidromeAuth struct {
	target *url.URL
	client *http.Client
	mu     sync.Mutex
	valid  map[string]time.Time // ключ — параметры авторизации, значение — до какого момента верим
}

func newNavidromeAuth(target *url.URL, transport http.RoundTripper) *navidromeAuth {
	return &navidromeAuth{target: target, client: &http.Client{Transport: transport, Timeout: 5 * time.Second}, valid: map[string]time.Time{}}
}

var authParams = []string{"u", "t", "s", "p", "apiKey"}

func (a *navidromeAuth) check(r *http.Request) bool {
	if r.Method == http.MethodPost && r.Form == nil && r.Body != nil {
		r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
		_ = r.ParseForm()
	}
	q := r.URL.Query()
	getParam := func(k string) string {
		if v := q.Get(k); v != "" {
			return v
		}
		return r.FormValue(k)
	}
	parts := make([]string, len(authParams))
	for i, k := range authParams {
		parts[i] = getParam(k)
	}
	key := strings.Join(parts, "\x00")
	if strings.Trim(key, "\x00") == "" {
		return false
	}

	now := time.Now()
	a.mu.Lock()
	until, ok := a.valid[key]
	a.mu.Unlock()
	if ok && now.Before(until) {
		return true
	}

	ping := url.Values{}
	for _, k := range append(append([]string(nil), authParams...), "v", "c") {
		if v := getParam(k); v != "" {
			ping.Set(k, v)
		}
	}
	if ping.Get("v") == "" {
		ping.Set("v", "1.16.1")
	}
	if ping.Get("c") == "" {
		ping.Set("c", "poisq")
	}
	ping.Set("f", "json")
	u := a.target.JoinPath("rest", "ping")
	u.RawQuery = ping.Encode()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		R struct {
			Status string `json:"status"`
		} `json:"subsonic-response"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil || body.R.Status != "ok" {
		return false
	}

	a.mu.Lock()
	if len(a.valid) >= 1000 { // чистим просроченные; если всё ещё >= 1000, сбрасываем карту целиком
		for k, t := range a.valid {
			if now.After(t) {
				delete(a.valid, k)
			}
		}
		if len(a.valid) >= 1000 {
			clear(a.valid)
		}
	}
	a.valid[key] = now.Add(navAuthTTL)
	a.mu.Unlock()
	return true
}

// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл auth_sniffer.go безопасно кэширует в оперативной памяти последние валидные
// Subsonic-параметры авторизации (u, t, s, v, c) или заголовок X-ND-Authorization,
// проходящие через прокси :4534. Это позволяет внутреннему серверу админки (:4535)
// бесшовно запрашивать обложки (/rest/getCoverArt) и аудиопотоки (/rest/stream) у Navidrome,
// даже если папка /music примонтирована по другому пути.
package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

type cachedSubsonicAuth struct {
	mu       sync.RWMutex
	query    url.Values
	ndAuth   string
	jwtToken string
}

var globalAuthCache = &cachedSubsonicAuth{
	query: make(url.Values),
}

// CaptureSubsonicAuth сохраняет авторизационные параметры из успешного входящего запроса к Navidrome.
func CaptureSubsonicAuth(req *http.Request) {
	if req == nil {
		return
	}
	captureSubsonicAuthFromValues(req.URL.Query(), req.Header.Get("X-ND-Authorization"))
}

// captureSubsonicAuthFromValues сохраняет авторизационные параметры из query/form и/или заголовка X-ND-Authorization,
// не смешивая между собой разные схемы и сессии авторизации.
func captureSubsonicAuthFromValues(q url.Values, ndAuth string) {
	u := q.Get("u")
	t := q.Get("t")
	s := q.Get("s")
	p := q.Get("p")
	apiKey := q.Get("apiKey")

	hasSubsonicAuth := (u != "" && (t != "" || p != "")) || apiKey != ""
	if !hasSubsonicAuth && ndAuth == "" {
		return
	}

	globalAuthCache.mu.Lock()
	defer globalAuthCache.mu.Unlock()

	if hasSubsonicAuth {
		// Набор заменяется целиком: иначе u одного клиента смешается с t/s или X-ND-Authorization другого.
		fresh := url.Values{}
		if apiKey != "" {
			fresh.Set("apiKey", apiKey)
		} else {
			fresh.Set("u", u)
			if t != "" {
				fresh.Set("t", t)
				fresh.Set("s", s)
			} else {
				fresh.Set("p", p)
			}
		}
		v := q.Get("v")
		if v == "" {
			v = "1.16.1"
		}
		fresh.Set("v", v)
		c := q.Get("c")
		if c == "" {
			c = "poisq"
		}
		fresh.Set("c", c)
		globalAuthCache.query = fresh
		globalAuthCache.ndAuth = ""
		return
	}

	if ndAuth != "" {
		globalAuthCache.ndAuth = ndAuth
		globalAuthCache.query = nil
	}
}

// captureSubsonicAuthOnSuccess сохраняет авторизационные данные только после подтверждения
// их валидности успешным ответом от Navidrome (HTTP 200 и отсутствие status="failed").
func captureSubsonicAuthOnSuccess(resp *http.Response) {
	if resp == nil || resp.Request == nil || resp.StatusCode != http.StatusOK {
		return
	}
	req := resp.Request
	q := req.URL.Query()
	hasQueryCreds := (q.Get("u") != "" && (q.Get("t") != "" || q.Get("p") != "")) || q.Get("apiKey") != ""
	ndAuth := req.Header.Get("X-ND-Authorization")
	if !hasQueryCreds && ndAuth == "" {
		return
	}

	path := strings.ToLower(req.URL.Path)
	if strings.HasPrefix(path, "/api/") {
		captureSubsonicAuthFromValues(q, ndAuth)
		return
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "audio/") || strings.HasPrefix(ct, "video/") ||
		strings.HasPrefix(ct, "image/") || strings.Contains(ct, "octet-stream") {
		captureSubsonicAuthFromValues(q, ndAuth)
		return
	}

	if resp.Body == nil || resp.Header.Get("Content-Encoding") != "" {
		return
	}

	origBody := resp.Body
	prefix, readErr := io.ReadAll(io.LimitReader(origBody, 1024))
	resp.Body = &prefixedReadCloser{
		Reader: io.MultiReader(bytes.NewReader(prefix), origBody),
		Closer: origBody,
	}
	if readErr != nil && len(prefix) == 0 {
		return
	}

	low := strings.ToLower(string(prefix))
	if strings.Contains(low, `"status":"failed"`) ||
		strings.Contains(low, `"status": "failed"`) ||
		strings.Contains(low, `status="failed"`) {
		return
	}
	if strings.Contains(low, `"status":"ok"`) ||
		strings.Contains(low, `"status": "ok"`) ||
		strings.Contains(low, `status="ok"`) {
		captureSubsonicAuthFromValues(q, ndAuth)
	}
}

// ApplyCachedAuthToRequest добавляет сохраненные учетные данные Subsonic к внутреннему запросу к Navidrome.
func ApplyCachedAuthToRequest(req *http.Request) bool {
	globalAuthCache.mu.RLock()
	defer globalAuthCache.mu.RUnlock()

	applied := false
	if len(globalAuthCache.query) > 0 {
		q := req.URL.Query()
		for k, vals := range globalAuthCache.query {
			if len(vals) > 0 && q.Get(k) == "" {
				q.Set(k, vals[0])
			}
		}
		req.URL.RawQuery = q.Encode()
		applied = true
	}
	if globalAuthCache.ndAuth != "" && req.Header.Get("X-ND-Authorization") == "" {
		req.Header.Set("X-ND-Authorization", globalAuthCache.ndAuth)
		applied = true
	}
	return applied
}

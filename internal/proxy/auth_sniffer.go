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
	"net/http"
	"net/url"
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
	q := req.URL.Query()
	u := q.Get("u")
	t := q.Get("t")
	s := q.Get("s")
	p := q.Get("p")
	ndAuth := req.Header.Get("X-ND-Authorization")

	if (u != "" && (t != "" || p != "")) || ndAuth != "" {
		globalAuthCache.mu.Lock()
		if u != "" {
			// Набор заменяется целиком: иначе u одного клиента смешается с t/s другого, и Navidrome отклонит такой вход.
			fresh := url.Values{"u": {u}}
			if t != "" {
				fresh.Set("t", t)
				fresh.Set("s", s)
			} else {
				fresh.Set("p", p)
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
		}
		if ndAuth != "" {
			globalAuthCache.ndAuth = ndAuth
		}
		globalAuthCache.mu.Unlock()
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

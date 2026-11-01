// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл interceptor.go перехватывает запросы к эндпоинтам поиска Subsonic API (search, search2, search3),
// OpenSubsonic и нативного UI Navidrome/Feishin (/api/search, /api/song, /api/album, /api/artist)
// как для GET, так и для POST-методов, обеспечивая интеллектуальный рерайтинг и логирование.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/lyrics"
	"github.com/F5GO/poisq/internal/search"
)

// Interceptor обрабатывает входящие HTTP-запросы, определяя необходимость модификации поисковых параметров.
type Interceptor struct {
	engine       *search.Engine
	fanoutMgr    *FanoutManager
	reverseProxy *httputil.ReverseProxy
	lyricsEngine *lyrics.Engine
}

// NewInterceptor создает новый перехватчик запросов.
func NewInterceptor(engine *search.Engine, fanoutMgr *FanoutManager, reverseProxy *httputil.ReverseProxy, lyricsEngine *lyrics.Engine) *Interceptor {
	return &Interceptor{
		engine:       engine,
		fanoutMgr:    fanoutMgr,
		reverseProxy: reverseProxy,
		lyricsEngine: lyricsEngine,
	}
}

// ServeHTTP анализирует путь запроса и при необходимости выполняет умную обработку.
func (it *Interceptor) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Предварительная обработка CORS preflight запросов для браузерных клиентов
	if req.Method == http.MethodOptions {
		origin := req.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-ND-Client-Unique-Id, X-ND-Authorization")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	path := strings.ToLower(req.URL.Path)

	// 1. Проверка Subsonic поиска (search3, search2, search, а также расширения .view)
	if isSubsonicSearchPath(path) {
		it.handleSubsonicSearch(w, req)
		return
	}

	// 2. Проверка Subsonic / OpenSubsonic текстов песен (getLyrics, getLyricsBySongId)
	if isSubsonicLyricsPath(path) {
		it.handleSubsonicLyrics(w, req)
		return
	}

	// 3. Проверка Native UI / Feishin поиска (/api/search, /api/song, /api/album, /api/artist)
	if isNativeAPIPath(path) {
		it.handleNativeSearch(w, req)
		return
	}

	// 3. Диагностическое логирование запросов аутентификации и сессий для проверки сети и CDN
	if strings.HasPrefix(path, "/auth/") || strings.HasPrefix(path, "/api/auth") || path == "/auth" {
		clientIP := ExtractClientIP(req)
		log.Printf("[AUTH] [%s] %s (Host: %s, Origin: %s, IP: %s, Proto: %s)",
			req.Method, req.URL.Path, req.Host, req.Header.Get("Origin"), clientIP, req.Header.Get("X-Forwarded-Proto"))
	}

	it.reverseProxy.ServeHTTP(w, req)
}

func isSubsonicSearchPath(path string) bool {
	return strings.Contains(path, "/rest/search3") ||
		strings.Contains(path, "/rest/search2") ||
		strings.Contains(path, "/rest/search")
}

func isNativeAPIPath(path string) bool {
	return strings.Contains(path, "/api/search") ||
		strings.HasPrefix(path, "/api/song") ||
		strings.HasPrefix(path, "/api/album") ||
		strings.HasPrefix(path, "/api/artist")
}

func (it *Interceptor) handleSubsonicSearch(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	q := req.URL.Query()

	// Ищем поисковый запрос в параметрах query, q, any
	paramName := "query"
	rawQuery := q.Get("query")
	if rawQuery == "" {
		if q.Get("q") != "" {
			paramName = "q"
			rawQuery = q.Get("q")
		} else if q.Get("any") != "" {
			paramName = "any"
			rawQuery = q.Get("any")
		}
	}

	// Поддержка POST-запросов со строкой поиска в теле (application/x-www-form-urlencoded)
	var isFormBody bool
	var formBytes []byte
	if rawQuery == "" && req.Method == http.MethodPost {
		contentType := req.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/x-www-form-urlencoded") {
			body, err := io.ReadAll(io.LimitReader(req.Body, 1024*1024))
			if err == nil {
				parsedForm, err := url.ParseQuery(string(body))
				if err == nil {
					if v := parsedForm.Get("query"); v != "" {
						paramName = "query"
						rawQuery = v
						isFormBody = true
					} else if v := parsedForm.Get("q"); v != "" {
						paramName = "q"
						rawQuery = v
						isFormBody = true
					} else if v := parsedForm.Get("any"); v != "" {
						paramName = "any"
						rawQuery = v
						isFormBody = true
					}
					formBytes = body
					req.Body = io.NopCloser(bytes.NewReader(body))
				}
			}
		}
	}

	if strings.TrimSpace(rawQuery) == "" {
		it.reverseProxy.ServeHTTP(w, req)
		return
	}

	clientIP := ExtractClientIP(req)

	res := it.engine.ProcessQuery(rawQuery)
	elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0
	log.Printf("[REWRITE] [%s] %s=%q -> %q [%.2fms] (Agent: %s, IP: %s, Path: %s)",
		req.Method, paramName, rawQuery, res.RewrittenQuery, elapsedMs, req.UserAgent(), clientIP, req.URL.Path)

	decorateTitles := (rawQuery != "" && res.RewrittenQuery != rawQuery)

	// Формируем расширенный список запросов для широкого и точного параллельного поиска
	queries := search.ExpandQuery(rawQuery, &res)
	if len(queries) > 1 || res.RewrittenQuery != rawQuery || res.NeedsFanout {
		it.fanoutMgr.ExecuteMultiFanout(w, req, queries, paramName, rawQuery, decorateTitles)
		return
	}

	// Одиночный рерайт: обновляем поисковый параметр в нижнем регистре с сохранением остальных параметров
	navidromeQuery := strings.ToLower(res.RewrittenQuery)
	if isFormBody {
		parsedForm, _ := url.ParseQuery(string(formBytes))
		parsedForm.Set(paramName, navidromeQuery)
		newBody := parsedForm.Encode()
		req.Body = io.NopCloser(strings.NewReader(newBody))
		req.ContentLength = int64(len(newBody))
		req.Header.Set("Content-Length", strconv.Itoa(len(newBody)))
	} else {
		q.Set(paramName, navidromeQuery)
		req.URL.RawQuery = q.Encode()
		req.RequestURI = req.URL.RequestURI()
	}

	it.reverseProxy.ServeHTTP(w, req)
}

func (it *Interceptor) handleNativeSearch(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()

	// Ищем параметры фильтрации native UI: "title", "name", "_search" или "query" / "q",
	// а также JSON-объект filter={"q":"..."} или filter={"_search":"..."}, используемый react-admin
	var isFilterJSON bool
	var filterMap map[string]interface{}
	var filterKey string
	var filterVal string

	targetParam := ""
	if q.Get("title") != "" {
		targetParam = "title"
	} else if q.Get("name") != "" {
		targetParam = "name"
	} else if q.Get("_search") != "" {
		targetParam = "_search"
	} else if q.Get("query") != "" {
		targetParam = "query"
	} else if q.Get("q") != "" {
		targetParam = "q"
	} else if f := q.Get("filter"); f != "" {
		if err := json.Unmarshal([]byte(f), &filterMap); err == nil {
			for _, k := range []string{"_search", "q", "title", "name", "query"} {
				if v, ok := filterMap[k].(string); ok && strings.TrimSpace(v) != "" {
					isFilterJSON = true
					filterKey = k
					filterVal = v
					targetParam = "filter." + k
					break
				}
			}
		}
	}

	if targetParam == "" {
		it.reverseProxy.ServeHTTP(w, req)
		return
	}

	clientIP := ExtractClientIP(req)

	start := time.Now()
	val := filterVal
	if !isFilterJSON {
		val = q.Get(targetParam)
	}
	res := it.engine.ProcessQuery(val)
	elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0
	log.Printf("[REWRITE-NATIVE] [%s] %s=%q -> %q [%.2fms] (Agent: %s, IP: %s, Path: %s)",
		req.Method, targetParam, val, res.RewrittenQuery, elapsedMs, req.UserAgent(), clientIP, req.URL.Path)

	if isFilterJSON {
		filterMap[filterKey] = res.RewrittenQuery
		if newFilterBytes, err := json.Marshal(filterMap); err == nil {
			q.Set("filter", string(newFilterBytes))
		}
	} else {
		q.Set(targetParam, res.RewrittenQuery)
	}
	req.URL.RawQuery = q.Encode()
	req.RequestURI = req.URL.RequestURI()

	it.reverseProxy.ServeHTTP(w, req)
}

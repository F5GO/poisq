// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл fanout.go реализует параллельный опрос Navidrome двумя вариантами запроса
// (например, канонический транслит и альтернативная раскладка) с защитой памяти (io.LimitReader),
// надежным автоопределением JSON/XML, фильтрацией hop-by-hop заголовков,
// высокоскоростным кэшированием ответов и дедупликацией параллельных запросов через SingleFlight.
package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/cache"
)

// FanoutManager координирует параллельные запросы к Navidrome с кэшированием и дедупликацией.
type FanoutManager struct {
	targetURL    *url.URL
	httpClient   *http.Client
	cache        *cache.MemoryCache
	singleflight *cache.SingleFlight
}

// NewFanoutManager создает новый экземпляр FanoutManager с общим пулом соединений и кэшем.
func NewFanoutManager(targetURL *url.URL, transport http.RoundTripper, memCache *cache.MemoryCache) *FanoutManager {
	if memCache == nil {
		memCache = cache.NewMemoryCache(1000, 30*time.Second)
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &FanoutManager{
		targetURL: targetURL,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
		},
		cache:        memCache,
		singleflight: cache.NewSingleFlight(),
	}
}

func parseCount(param string) int {
	if param == "" {
		return 20
	}
	n, err := strconv.Atoi(param)
	if err != nil || n < 0 {
		return 20
	}
	return min(n, maxSearchCount) // один запрос не должен вытягивать всю медиатеку в память, умноженную на число вариантов
}

const maxSearchCount = 500

// authKey — отпечаток всех данных входа: кэш и склейка параллельных запросов не должны отдавать ответ одного входа другому.
func authKey(req *http.Request) string {
	q := req.URL.Query()
	h := sha256.Sum256([]byte(strings.Join([]string{q.Get("u"), q.Get("t"), q.Get("s"), q.Get("p"), q.Get("apiKey"),
		req.Header.Get("X-ND-Authorization"), req.Header.Get("Authorization")}, "\x00")))
	return fmt.Sprintf("%x", h[:16])
}

// ExecuteMultiFanout выполняет параллельный N-сторонний опрос Navidrome по списку релевантных запросов,
// объединяет и дедуплицирует результаты, обеспечивая максимально широкую и точную поисковую выдачу.
func (f *FanoutManager) ExecuteMultiFanout(w http.ResponseWriter, req *http.Request, queries []string, paramName, rawQuery string, decorateTitles bool) {
	if len(queries) == 0 {
		return
	}

	origQuery := req.URL.Query()

	artistCount := parseCount(origQuery.Get("artistCount"))
	albumCount := parseCount(origQuery.Get("albumCount"))
	songCount := parseCount(origQuery.Get("songCount"))
	format := strings.ToLower(origQuery.Get("f"))

	cacheKey := fmt.Sprintf("%s|%s|%s|%s|%d|%d|%d|%v|%s",
		req.Method, strings.Join(queries, "##"), format, authKey(req), artistCount, albumCount, songCount, decorateTitles, rawQuery)

	// 1. Проверяем кэш в оперативной памяти (ответ за <0.05ms)
	if f.cache != nil {
		if cachedBody, cachedHdr, cachedCode, ok := f.cache.Get(cacheKey); ok {
			isJSON := strings.Contains(strings.ToLower(cachedHdr.Get("Content-Type")), "json")
			log.Printf("[CACHE-HIT] queries=%v (%d bytes) [0.05ms]", queries, len(cachedBody))
			f.writeResponse(w, cachedBody, cachedCode, cachedHdr, isJSON)
			return
		}
	}

	fetchAndMerge := func() ([]byte, http.Header, int, bool, error) {
		ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
		defer cancel()

		n := len(queries)
		responses := make([][]byte, n)
		headersList := make([]http.Header, n)
		codes := make([]int, n)
		errors := make([]error, n)

		var wg sync.WaitGroup
		for i, qVal := range queries {
			i, qVal := i, qVal
			wg.Add(1)
			go func() {
				defer wg.Done()
				responses[i], codes[i], headersList[i], errors[i] = f.fetchSubsonic(ctx, req, qVal, paramName)
			}()
		}
		wg.Wait()

		var firstValidHeader http.Header
		firstValidCode := http.StatusOK
		isJSON := false
		hasAnySuccess := false

		for i := 0; i < n; i++ {
			if errors[i] == nil && len(responses[i]) > 0 {
				hasAnySuccess = true
				if firstValidHeader == nil {
					firstValidHeader = headersList[i]
					firstValidCode = codes[i]
				}
				hdr := headersList[i]
				if hdr != nil && strings.Contains(strings.ToLower(hdr.Get("Content-Type")), "json") {
					isJSON = true
				} else {
					trimmed := bytes.TrimSpace(responses[i])
					if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
						isJSON = true
					}
				}
			}
		}

		if !hasAnySuccess {
			return nil, nil, http.StatusBadGateway, false, fmt.Errorf("все запросы к Navidrome завершились с ошибкой: %v", errors)
		}

		if strings.ToLower(origQuery.Get("f")) == "json" || strings.Contains(strings.ToLower(req.Header.Get("Accept")), "json") {
			isJSON = true
		}

		var merged []byte
		var mergeErr error

		if isJSON {
			merged, mergeErr = MergeMultipleJSONResponses(responses, artistCount, albumCount, songCount, rawQuery, decorateTitles)
		} else {
			merged, mergeErr = MergeMultipleXMLResponses(responses, artistCount, albumCount, songCount, rawQuery, decorateTitles)
		}

		if mergeErr != nil || len(merged) == 0 {
			// В случае ошибки слияния отдаем первый успешный ответ
			for i := 0; i < n; i++ {
				if len(responses[i]) > 0 {
					return responses[i], headersList[i], codes[i], isJSON, nil
				}
			}
		}

		log.Printf("[MULTI-FANOUT] queries=%v -> merged=%d bytes (isJSON=%v, code=%d)",
			queries, len(merged), isJSON, firstValidCode)

		return merged, firstValidHeader, http.StatusOK, isJSON, nil
	}

	var (
		merged        []byte
		mergedHeaders http.Header
		mergedCode    int
		isJSON        bool
		err           error
	)

	// 2. Дедупликация параллельных запросов через SingleFlight
	if f.singleflight != nil {
		resBody, resHdr, resCode, resErr := f.singleflight.Do(cacheKey, func() ([]byte, http.Header, int, error) {
			b, h, c, j, e := fetchAndMerge()
			if e != nil {
				return nil, nil, c, e
			}
			if j && (h == nil || !strings.Contains(strings.ToLower(h.Get("Content-Type")), "json")) {
				if h == nil {
					h = make(http.Header)
				}
				h.Set("Content-Type", "application/json; charset=utf-8")
			}
			return b, h, c, nil
		})
		merged = resBody
		mergedHeaders = resHdr
		mergedCode = resCode
		err = resErr
		if mergedHeaders != nil && strings.Contains(strings.ToLower(mergedHeaders.Get("Content-Type")), "json") {
			isJSON = true
		}
	} else {
		merged, mergedHeaders, mergedCode, isJSON, err = fetchAndMerge()
	}

	if err != nil {
		log.Printf("[ERROR] Запрос к Navidrome завершился с ошибкой: %v", err)
		http.Error(w, "Ошибка вышестоящего сервера Navidrome", http.StatusBadGateway)
		return
	}

	// 3. Сохраняем в кэш на 30 секунд для мгновенных повторных ответов
	if f.cache != nil && mergedCode == http.StatusOK {
		f.cache.Set(cacheKey, merged, mergedHeaders, mergedCode, 30*time.Second)
	}

	f.writeResponse(w, merged, mergedCode, mergedHeaders, isJSON)
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// Hop-by-hop заголовки HTTP/1.1 (RFC 7230, section 6.1), которые не должны проксироваться дальше.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailers":            true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"content-encoding":    true,
	"content-length":      true,
}

func (f *FanoutManager) fetchSubsonic(ctx context.Context, origReq *http.Request, queryVal, paramName string) ([]byte, int, http.Header, error) {
	targetURL := *f.targetURL
	targetURL.Path = singleJoiningSlash(f.targetURL.Path, origReq.URL.Path)

	q := origReq.URL.Query()
	// Приводим поисковый запрос к нижнему регистру для Navidrome:
	// в Navidrome full_text хранится в lowercase, а SQLite LIKE регистрозависим для не-ASCII (кириллицы)
	navidromeQuery := strings.ToLower(queryVal)
	q.Set(paramName, navidromeQuery)
	targetURL.RawQuery = q.Encode()

	// Для Subsonic fan-out используем метод GET к Navidrome для надежного чтения данных
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return nil, 0, nil, err
	}

	// Копируем необходимые заголовки (авторизация, cookie, user-agent, accept)
	// Исключаем Accept-Encoding, чтобы Navidrome вернул несжатый ответ (identity) для корректного слияния JSON/XML
	for key, values := range origReq.Header {
		if strings.EqualFold(key, "Host") ||
			strings.EqualFold(key, "Content-Length") ||
			strings.EqualFold(key, "Content-Type") ||
			strings.EqualFold(key, "Accept-Encoding") {
			continue
		}
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}
	req.Header.Set("Accept-Encoding", "identity")

	origHost := ExtractClientHost(origReq)
	if origHost == "" {
		origHost = f.targetURL.Host
	}
	req.Host = origHost
	req.Header.Set("X-Forwarded-Host", origHost)

	clientIP := ExtractClientIP(origReq)
	req.Header.Set("X-Forwarded-For", clientIP)

	isHTTPS := origReq.TLS != nil ||
		strings.EqualFold(origReq.Header.Get("X-Forwarded-Proto"), "https") ||
		strings.Contains(origReq.Header.Get("CF-Visitor"), "https")

	if isHTTPS {
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Port", "443")
		req.Header.Set("X-Forwarded-Ssl", "on")
	} else if origReq.Header.Get("X-Forwarded-Proto") != "" {
		req.Header.Set("X-Forwarded-Proto", origReq.Header.Get("X-Forwarded-Proto"))
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()

	// Защита от истощения памяти (DoS/Memory Exhaustion): ограничиваем ответ от Navidrome до 10 МБ
	limitedReader := io.LimitReader(resp.Body, 10*1024*1024)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, resp.StatusCode, resp.Header, err
	}

	return body, resp.StatusCode, resp.Header, nil
}

func (f *FanoutManager) writeResponse(w http.ResponseWriter, body []byte, statusCode int, upstreamHeaders http.Header, isJSON bool) {
	// Проксируем метаданные от Navidrome (CORS, кэш, Subsonic метаданные), исключая hop-by-hop заголовки
	if upstreamHeaders != nil {
		for key, values := range upstreamHeaders {
			lowerKey := strings.ToLower(key)
			if hopByHopHeaders[lowerKey] || strings.EqualFold(key, "Content-Type") {
				continue
			}
			for _, v := range values {
				w.Header().Add(key, v)
			}
		}
	}

	if isJSON {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
}

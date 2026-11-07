// Package proxy реализует прозрачный HTTP Reverse Proxy для Navidrome
// с перехватом поисковых запросов и fan-out объединением результатов.
//
// Данный файл proxy_test.go тестирует объединение и дедупликацию ответов
// Subsonic API (JSON и XML), а также работу диагностического эндпоинта /_smart/test.
package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/cache"
	"github.com/F5GO/poisq/internal/search"
)

func TestMergeJSONSearchResponses(t *testing.T) {
	respA := []byte(`{
		"subsonic-response": {
			"status": "ok",
			"version": "1.16.1",
			"searchResult3": {
				"artist": [
					{"id": "art-1", "name": "Nirvana"},
					{"id": "art-2", "name": "Offspring"}
				],
				"song": [
					{"id": "s-1", "title": "Smells Like Teen Spirit"}
				]
			}
		}
	}`)

	respB := []byte(`{
		"subsonic-response": {
			"status": "ok",
			"version": "1.16.1",
			"searchResult3": {
				"artist": [
					{"id": "art-2", "name": "Offspring"},
					{"id": "art-3", "name": "Queen"}
				],
				"song": [
					{"id": "s-1", "title": "Smells Like Teen Spirit"},
					{"id": "s-2", "title": "Bohemian Rhapsody"}
				]
			}
		}
	}`)

	merged, err := MergeMultipleJSONResponses([][]byte{respA, respB}, 10, 10, 10, "", false)
	if err != nil {
		t.Fatalf("Ошибка MergeJSONSearchResponses: %v", err)
	}

	var parsed SubsonicJSONEnvelope
	if err := json.Unmarshal(merged, &parsed); err != nil {
		t.Fatalf("Ошибка парсинга объединенного JSON: %v", err)
	}

	rawRes, ok := parsed.SubsonicResponse["searchResult3"]
	if !ok {
		t.Fatalf("searchResult3 пуст")
	}

	var res SubsonicResult
	if err := json.Unmarshal(rawRes, &res); err != nil {
		t.Fatalf("Ошибка парсинга searchResult3: %v", err)
	}

	// Ожидаем дедупликацию: art-1, art-2, art-3 (3 артиста)
	if len(res.Artist) != 3 {
		t.Errorf("Ожидалось 3 уникальных артиста, получено %d", len(res.Artist))
	}

	// Ожидаем дедупликацию треков: s-1, s-2 (2 трека)
	if len(res.Song) != 2 {
		t.Errorf("Ожидалось 2 уникальных трека, получено %d", len(res.Song))
	}
}

func TestMergeXMLSearchResponses(t *testing.T) {
	xmlA := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1">
  <searchResult3>
    <artist id="art-1" name="Nirvana"/>
    <artist id="art-2" name="The Offspring"/>
    <song id="s-1" title="In Bloom"/>
  </searchResult3>
</subsonic-response>`)

	xmlB := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1">
  <searchResult3>
    <artist id="art-2" name="The Offspring"/>
    <artist id="art-3" name="Kino"/>
    <song id="s-1" title="In Bloom"/>
    <song id="s-2" title="Blood Type"/>
  </searchResult3>
</subsonic-response>`)

	merged, err := MergeMultipleXMLResponses([][]byte{xmlA, xmlB}, 10, 10, 10, "", false)
	if err != nil {
		t.Fatalf("Ошибка MergeXMLSearchResponses: %v", err)
	}

	strMerged := string(merged)
	t.Logf("Merged XML:\n%s", strMerged)
	if !strings.Contains(strMerged, "art-1") || !strings.Contains(strMerged, "art-2") || !strings.Contains(strMerged, "art-3") {
		t.Errorf("Объединенный XML не содержит всех артистов: %s", strMerged)
	}

	// Проверяем дедупликацию: art-2 должен встречаться только 1 раз в теге artist
	if strings.Count(strMerged, `id="art-2"`) != 1 {
		t.Errorf("Артист art-2 не дедуплицирован в XML: %s", strMerged)
	}

	// Проверяем трек s-1
	if strings.Count(strMerged, `id="s-1"`) != 1 {
		t.Errorf("Трек s-1 не дедуплицирован в XML: %s", strMerged)
	}
}

func TestDebugTestEndpoint(t *testing.T) {
	dict := search.NewDictionary()
	dict.Rebuild([]search.DictItem{
		{Text: "The Offspring", EntityType: "artist", PlayCount: 100},
	})
	engine := search.NewEngine(dict)

	handler := HandleDebugTest(engine, dict)

	req := httptest.NewRequest("GET", "/_smart/test?q=щеаызкштп", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Ожидался статус 200, получен %d", rr.Code)
	}

	var resp DebugResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Ошибка декодирования ответа: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("Ожидался status ok, получено %s", resp.Status)
	}

	if !strings.EqualFold(resp.RewrittenQuery, "The Offspring") && !strings.EqualFold(resp.RewrittenQuery, "offspring") {
		t.Errorf("Ожидалось переписывание в Offspring, получено: %s", resp.RewrittenQuery)
	}
}

func TestAliasesAPIEndpoint(t *testing.T) {
	dict := search.NewDictionary()
	aliasMgr := search.NewAliasManager("", "", dict)

	handler := HandleAliasesAPI(aliasMgr)

	// 1. Тест GET /_smart/aliases (безопасное чтение разрешено)
	reqGet := httptest.NewRequest(http.MethodGet, "/_smart/aliases", nil)
	rrGet := httptest.NewRecorder()
	handler.ServeHTTP(rrGet, reqGet)

	if rrGet.Code != http.StatusOK {
		t.Fatalf("GET /_smart/aliases: статус %d", rrGet.Code)
	}

	var getResp struct {
		Status  string            `json:"status"`
		Count   int               `json:"count"`
		Aliases map[string]string `json:"aliases"`
	}
	if err := json.Unmarshal(rrGet.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("Ошибка парсинга ответа GET: %v", err)
	}
	if getResp.Count < 50 {
		t.Errorf("Ожидалось не менее 50 алиасов, получено: %d", getResp.Count)
	}

	// 2. Тест POST /_smart/aliases (мутация через HTTP запрещена и возвращает 405 Method Not Allowed)
	body := strings.NewReader(`{"alias": "гражданка", "target": "Гражданская Оборона"}`)
	reqPost := httptest.NewRequest(http.MethodPost, "/_smart/aliases", body)
	rrPost := httptest.NewRecorder()
	handler.ServeHTTP(rrPost, reqPost)

	if rrPost.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Ожидался статус 405 Method Not Allowed для небезопасного POST, получено: %d", rrPost.Code)
	}
}

func TestServerDebugAPIAccessControl(t *testing.T) {
	dict := search.NewDictionary()
	engine := search.NewEngine(dict)
	aliasMgr := search.NewAliasManager("", "", dict)

	dummyURL, _ := http.DefaultClient.Transport, "http://127.0.0.1:4533"
	_ = dummyURL
	u, _ := reqGetURL("http://127.0.0.1:4533")

	// 1. При EnableDebugAPI = false (production режим по умолчанию)
	prodServer := NewServer(u, engine, dict, aliasMgr, false)
	req1 := httptest.NewRequest(http.MethodGet, "/_smart/aliases", nil)
	rr1 := httptest.NewRecorder()
	prodServer.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusNotFound {
		t.Errorf("В продакшн режиме /_smart/aliases должен возвращать 404, получено: %d", rr1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/_smart/test?q=test", nil)
	rr2 := httptest.NewRecorder()
	prodServer.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusNotFound {
		t.Errorf("В продакшн режиме /_smart/test должен возвращать 404, получено: %d", rr2.Code)
	}

	// 2. При EnableDebugAPI = true (диагностический режим)
	debugServer := NewServer(u, engine, dict, aliasMgr, true)
	req3 := httptest.NewRequest(http.MethodGet, "/_smart/aliases", nil)
	rr3 := httptest.NewRecorder()
	debugServer.ServeHTTP(rr3, req3)

	if rr3.Code != http.StatusOK {
		t.Errorf("В debug режиме /_smart/aliases должен возвращать 200, получено: %d", rr3.Code)
	}
}

func TestInterceptorVariations(t *testing.T) {
	// Проверка сопоставления путей
	if !isSubsonicSearchPath("/rest/search.view") {
		t.Errorf("Ожидалось совпадение для /rest/search.view")
	}
	if !isSubsonicSearchPath("/rest/search3.view") {
		t.Errorf("Ожидалось совпадение для /rest/search3.view")
	}
	if !isSubsonicSearchPath("/subsonic/rest/search3") {
		t.Errorf("Ожидалось совпадение для /subsonic/rest/search3")
	}
	if isSubsonicSearchPath("/rest/getMusicFolders") {
		t.Errorf("Не должно совпадать для /rest/getMusicFolders")
	}

	// Проверка нативного API
	if !isNativeAPIPath("/api/search") {
		t.Errorf("Ожидалось совпадение для /api/search")
	}
	if !isNativeAPIPath("/api/song") {
		t.Errorf("Ожидалось совпадение для /api/song")
	}
	if !isNativeAPIPath("/api/songs") {
		t.Errorf("Ожидалось совпадение для /api/songs")
	}
	if !isNativeAPIPath("/api/album") {
		t.Errorf("Ожидалось совпадение для /api/album")
	}
	if !isNativeAPIPath("/api/artist") {
		t.Errorf("Ожидалось совпадение для /api/artist")
	}
}

func TestNativeSearchFilterJSON(t *testing.T) {
	dict := search.NewDictionary()
	dict.Rebuild([]search.DictItem{
		{Text: "Linkin Park", EntityType: "artist", PlayCount: 100},
	})
	engine := search.NewEngine(dict)
	targetURL, _ := url.Parse("http://127.0.0.1:4533")
	fanoutMgr := NewFanoutManager(targetURL, nil, nil)
	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	interceptor := NewInterceptor(engine, fanoutMgr, reverseProxy)

	req := httptest.NewRequest("GET", "/api/artist?filter=%7B%22_search%22%3A%22%D0%BB%D0%B8%D0%BD%D0%BA%D0%B8%D0%BD%22%7D", nil)
	rr := httptest.NewRecorder()

	interceptor.handleNativeSearch(rr, req)

	newFilter := req.URL.Query().Get("filter")
	if !strings.Contains(newFilter, "Linkin Park") {
		t.Errorf("Ожидалось переписывание в Linkin Park внутри JSON filter: %s", newFilter)
	}
}

func TestAmperfyXMLDecoration(t *testing.T) {
	xmlResp := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1">
  <searchResult3>
    <artist id="art-1" name="Green Day"/>
    <song id="s-1" title="Basket Case" artist="Green Day"/>
  </searchResult3>
</subsonic-response>`)

	decorated, err := MergeMultipleXMLResponses([][]byte{xmlResp, nil}, 10, 10, 10, "грин де", true)
	if err != nil {
		t.Fatalf("Ошибка MergeXMLSearchResponses для Amperfy: %v", err)
	}

	strResult := string(decorated)
	t.Logf("Amperfy Decorated XML:\n%s", strResult)

	if !strings.Contains(strResult, `name="Green Day (грин де)"`) {
		t.Errorf("Артист должен содержать поисковый токен (грин де) для Amperfy: %s", strResult)
	}
	if !strings.Contains(strResult, `title="Basket Case (грин де)"`) {
		t.Errorf("Трек должен содержать поисковый токен (грин де) для Amperfy: %s", strResult)
	}
}

func TestAmperfyJSONDecoration(t *testing.T) {
	jsonResp := []byte(`{
		"subsonic-response": {
			"status": "ok",
			"version": "1.16.1",
			"searchResult3": {
				"artist": [
					{"id": "art-1", "name": "Green Day"}
				],
				"song": [
					{"id": "s-1", "title": "Basket Case"}
				]
			}
		}
	}`)

	decorated, err := MergeMultipleJSONResponses([][]byte{jsonResp, nil}, 10, 10, 10, "грин де", true)
	if err != nil {
		t.Fatalf("Ошибка MergeJSONSearchResponses для Amperfy: %v", err)
	}

	strResult := string(decorated)
	t.Logf("Amperfy Decorated JSON:\n%s", strResult)

	if !strings.Contains(strResult, `"name":"Green Day (грин де)"`) {
		t.Errorf("Артист в JSON должен содержать поисковый токен (грин де) для Amperfy: %s", strResult)
	}
	if !strings.Contains(strResult, `"title":"Basket Case (грин де)"`) {
		t.Errorf("Трек в JSON должен содержать поисковый токен (грин де) для Amperfy: %s", strResult)
	}
}

func TestReverseProxyCDNHeaders(t *testing.T) {
	targetURL, _ := url.Parse("http://ix-navidrome-navidrome-1:4533")

	director := SetupDirector(targetURL, nil)

	req := httptest.NewRequest(http.MethodGet, "http://music.teedi.ru/auth/login", nil)
	req.Host = "music.teedi.ru"
	req.Header.Set("CF-Visitor", `{"scheme":"https"}`)
	req.Header.Set("CF-Connecting-IP", "203.0.113.195")

	director(req)

	// Проверяем сохранение Host и установку X-Forwarded-* заголовков для Navidrome
	if req.Host != "music.teedi.ru" {
		t.Errorf("req.Host должен сохранять клиентский домен music.teedi.ru, получено: %s", req.Host)
	}
	if req.Header.Get("X-Forwarded-Host") != "music.teedi.ru" {
		t.Errorf("X-Forwarded-Host должен быть music.teedi.ru, получено: %s", req.Header.Get("X-Forwarded-Host"))
	}
	if req.Header.Get("X-Forwarded-Proto") != "https" {
		t.Errorf("X-Forwarded-Proto должен быть https при CF-Visitor https, получено: %s", req.Header.Get("X-Forwarded-Proto"))
	}
	if req.Header.Get("X-Forwarded-Port") != "443" {
		t.Errorf("X-Forwarded-Port должен быть 443, получено: %s", req.Header.Get("X-Forwarded-Port"))
	}
	if req.Header.Get("X-Forwarded-Ssl") != "on" {
		t.Errorf("X-Forwarded-Ssl должен быть on, получено: %s", req.Header.Get("X-Forwarded-Ssl"))
	}
	if !strings.Contains(req.Header.Get("X-Forwarded-For"), "203.0.113.195") {
		t.Errorf("X-Forwarded-For должен содержать CF-Connecting-IP, получено: %s", req.Header.Get("X-Forwarded-For"))
	}
	if req.URL.Host != "ix-navidrome-navidrome-1:4533" {
		t.Errorf("req.URL.Host должен указывать на внутренний адрес целевого контейнера, получено: %s", req.URL.Host)
	}

	// Проверяем работу со сложной цепочкой прокси (CDN -> NPM -> FRP) с несколькими хостами через запятую
	reqCDN := httptest.NewRequest(http.MethodPost, "http://internal:4534/auth/login", nil)
	reqCDN.Header.Set("Origin", "https://muth.f5go.one")
	reqCDN.Header.Set("X-Forwarded-Host", "muth.f5go.one, 5724d059-9040-4452-origin-music.f5go.one")

	director(reqCDN)

	if reqCDN.Host != "muth.f5go.one" {
		t.Errorf("req.Host должен нормализоваться до muth.f5go.one из Origin, получено: %s", reqCDN.Host)
	}
	if reqCDN.Header.Get("X-Forwarded-Host") != "muth.f5go.one" {
		t.Errorf("X-Forwarded-Host должен быть muth.f5go.one, получено: %s", reqCDN.Header.Get("X-Forwarded-Host"))
	}
}

type mockRoundTripper struct {
	roundTripFunc func(req *http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTripFunc(req)
}

func TestFanoutCacheHit(t *testing.T) {
	var callCount int32
	mockTransport := &mockRoundTripper{
		roundTripFunc: func(req *http.Request) (*http.Response, error) {
			atomic.AddInt32(&callCount, 1)
			body := `{"subsonic-response":{"status":"ok","version":"1.16.1","searchResult3":{"artist":[{"id":"1","name":"Linkin Park"}]}}}`
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}
			resp.Header.Set("Content-Type", "application/json; charset=utf-8")
			return resp, nil
		},
	}

	targetURL, _ := url.Parse("http://navidrome-test:4533")
	memCache := cache.NewMemoryCache(100, 1*time.Minute)
	fanoutMgr := NewFanoutManager(targetURL, mockTransport, memCache)

	req1 := httptest.NewRequest("GET", "/rest/search3.view?query=Linkin+Park&f=json", nil)
	rr1 := httptest.NewRecorder()
	fanoutMgr.ExecuteMultiFanout(rr1, req1, []string{"Linkin Park"}, "query", "линк", false)

	if rr1.Code != http.StatusOK {
		t.Fatalf("Ожидался код 200, получено: %d", rr1.Code)
	}
	if atomic.LoadInt32(&callCount) != 1 {
		t.Fatalf("Ожидался 1 вызов upstream, получено: %d", callCount)
	}

	// Второй идентичный запрос должен быть взят из кэша (0 вызовов upstream)
	req2 := httptest.NewRequest("GET", "/rest/search3.view?query=Linkin+Park&f=json", nil)
	rr2 := httptest.NewRecorder()
	fanoutMgr.ExecuteMultiFanout(rr2, req2, []string{"Linkin Park"}, "query", "линк", false)

	if rr2.Code != http.StatusOK {
		t.Fatalf("Ожидался код 200 для кэшированного ответа, получено: %d", rr2.Code)
	}
	if atomic.LoadInt32(&callCount) != 1 {
		t.Fatalf("Ожидался cache hit без вызова upstream (счетчик должен остаться 1), получено: %d", callCount)
	}

	// Тот же пользователь с другим токеном — не из кэша: ответ одного входа не достаётся другому
	req3 := httptest.NewRequest("GET", "/rest/search3.view?query=Linkin+Park&f=json&u=alice&t=wrong&s=x", nil)
	fanoutMgr.ExecuteMultiFanout(httptest.NewRecorder(), req3, []string{"Linkin Park"}, "query", "линк", false)
	req4 := httptest.NewRequest("GET", "/rest/search3.view?query=Linkin+Park&f=json&u=alice&t=good&s=x", nil)
	fanoutMgr.ExecuteMultiFanout(httptest.NewRecorder(), req4, []string{"Linkin Park"}, "query", "линк", false)
	if atomic.LoadInt32(&callCount) != 3 {
		t.Fatalf("Запросы с разными данными входа должны идти в Navidrome отдельно, вызовов: %d", callCount)
	}
}

func reqGetURL(raw string) (*url.URL, error) {
	return url.Parse(raw)
}

// POST-поиск: вход лежит в теле, а fan-out ходит в Navidrome GET-запросами — вход должен дойти.
func TestPostSearchFanoutKeepsBodyAuth(t *testing.T) {
	dict := search.NewDictionary()
	dict.Rebuild([]search.DictItem{{Text: "Linkin Park", EntityType: "artist", PlayCount: 100}})
	var seen []url.Values
	var mu sync.Mutex
	upstream := &mockRoundTripper{roundTripFunc: func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, req.URL.Query())
		mu.Unlock()
		resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req,
			Body: io.NopCloser(strings.NewReader(`{"subsonic-response":{"status":"ok","searchResult3":{}}}`))}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	}}
	targetURL, _ := url.Parse("http://navidrome-test:4533")
	interceptor := NewInterceptor(search.NewEngine(dict), NewFanoutManager(targetURL, upstream, nil), httputil.NewSingleHostReverseProxy(targetURL))

	body := "u=alice&t=tok&s=salt&f=json&songCount=7&query=" + url.QueryEscape("дштлшт зфкл") // «linkin park» в русской раскладке
	req := httptest.NewRequest(http.MethodPost, "/rest/search3", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	interceptor.ServeHTTP(httptest.NewRecorder(), req)

	if len(seen) == 0 {
		t.Fatal("поиск не дошёл до fan-out")
	}
	for _, q := range seen {
		if q.Get("u") != "alice" || q.Get("t") != "tok" || q.Get("songCount") != "7" {
			t.Fatalf("fan-out потерял параметры из тела: %v", q)
		}
	}
}

// Вход одного клиента не смешивается с входом другого.
func TestCapturedAuthIsReplacedWhole(t *testing.T) {
	CaptureSubsonicAuth(httptest.NewRequest("GET", "/rest/ping?u=a&t=tok&s=salt", nil))
	CaptureSubsonicAuth(httptest.NewRequest("GET", "/rest/ping?u=b&p=pass", nil))
	req := httptest.NewRequest("GET", "/rest/getCoverArt?id=1", nil)
	ApplyCachedAuthToRequest(req)
	q := req.URL.Query()
	if q.Get("u") != "b" || q.Get("p") != "pass" || q.Get("t") != "" || q.Get("s") != "" {
		t.Fatalf("смешанный вход: %v", q)
	}
}

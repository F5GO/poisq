// Package proxy_test тестирует перехват и форматирование запросов текстов песен через прокси poisq.
//
// Данный файл interceptor_lyrics_test.go проверяет поведение эндпоинтов getLyrics, getLyricsBySongId,
// валидацию аутентификации Subsonic, совместимый LRCLIB API и диагностические эндпоинты /_smart/lyrics/*.
package proxy_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/lyrics"
	"github.com/F5GO/poisq/internal/proxy"
	"github.com/F5GO/poisq/internal/search"
)

type mockNavidromeRoundTripper struct {
	authSuccess bool
	lyricsXML   string
}

func (m *mockNavidromeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if !m.authSuccess {
		failXML := `<?xml version="1.0" encoding="UTF-8"?><subsonic-response xmlns="http://subsonic.org/restapi" status="failed" version="1.16.1"><error code="40" message="Wrong username or password"/></subsonic-response>`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(failXML)),
		}, nil
	}

	xmlBody := m.lyricsXML
	if xmlBody == "" {
		xmlBody = `<?xml version="1.0" encoding="UTF-8"?><subsonic-response xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1"><lyrics/></subsonic-response>`
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/xml"}},
		Body:       io.NopCloser(strings.NewReader(xmlBody)),
	}, nil
}

func TestProxy_SubsonicLyrics_SecurityAndAuth(t *testing.T) {
	lyricsCfg := &lyrics.Config{
		Enabled:  true,
		CacheTTL: 1 * time.Hour,
	}
	engine := lyrics.NewEngine(lyricsCfg, nil)

	// Добавляем песню в кэш
	engine.GetCache().Set("Кино", "Группа крови", &lyrics.Lyrics{
		Kind:         lyrics.KindLRC,
		SyncedLyrics: "[00:12.34] Тёплое место на улице ждут",
		PlainLyrics:  "Тёплое место на улице ждут",
		Source:       "yandex",
		CachedAt:     time.Now(),
	})

	targetURL, _ := url.Parse("http://navidrome.mock:4533")

	// 1. Проверка безопасности: неавторизованный запрос НЕ ДОЛЖЕН получить слова из кэша
	revProxyUnauth := httputil.NewSingleHostReverseProxy(targetURL)
	revProxyUnauth.Transport = &mockNavidromeRoundTripper{authSuccess: false}
	interceptorUnauth := proxy.NewInterceptor(nil, nil, revProxyUnauth, engine)

	reqUnauth := httptest.NewRequest(http.MethodGet, "/rest/getLyrics?u=baduser&p=badpass&artist=%D0%9A%D0%B8%D0%BD%D0%BE&title=%D0%93%D1%80%D1%83%D0%BF%D0%BF%D0%B0+%D0%BA%D1%80%D0%BE%D0%B2%D0%B8", nil)
	rrUnauth := httptest.NewRecorder()
	interceptorUnauth.ServeHTTP(rrUnauth, reqUnauth)

	if !strings.Contains(rrUnauth.Body.String(), `status="failed"`) || !strings.Contains(rrUnauth.Body.String(), `code="40"`) {
		t.Fatalf("Безопасность нарушена: неавторизованный запрос не был отклонен! Ответ: %s", rrUnauth.Body.String())
	}
	if strings.Contains(rrUnauth.Body.String(), "Тёплое место") {
		t.Fatalf("Утечка данных: неавторизованному клиенту были отданы слова песни!")
	}

	// 2. Проверка успешной авторизации: авторизованный клиент получает текст из кэша poisq
	revProxyAuth := httputil.NewSingleHostReverseProxy(targetURL)
	revProxyAuth.Transport = &mockNavidromeRoundTripper{authSuccess: true}
	interceptorAuth := proxy.NewInterceptor(nil, nil, revProxyAuth, engine)

	reqAuth := httptest.NewRequest(http.MethodGet, "/rest/getLyrics?u=admin&p=admin&artist=%D0%9A%D0%B8%D0%BD%D0%BE&title=%D0%93%D1%80%D1%83%D0%BF%D0%BF%D0%B0+%D0%BA%D1%80%D0%BE%D0%B2%D0%B8&f=json", nil)
	rrAuth := httptest.NewRecorder()
	interceptorAuth.ServeHTTP(rrAuth, reqAuth)

	if rrAuth.Code != http.StatusOK {
		t.Fatalf("Ожидался код 200, получено: %d", rrAuth.Code)
	}

	var data map[string]any
	if err := json.Unmarshal(rrAuth.Body.Bytes(), &data); err != nil {
		t.Fatalf("Ошибка парсинга JSON: %v", err)
	}

	subResp := data["subsonic-response"].(map[string]any)
	if subResp["status"] != "ok" {
		t.Errorf("Ожидался статус ok, получено: %v", subResp["status"])
	}

	lyrData := subResp["lyrics"].(map[string]any)
	if !strings.Contains(lyrData["value"].(string), "Тёплое место") {
		t.Errorf("Неверный текст в ответе: %v", lyrData["value"])
	}
}

func TestProxy_LRCLIB_CompatibleAPI(t *testing.T) {
	lyricsCfg := &lyrics.Config{
		Enabled:  true,
		CacheTTL: 1 * time.Hour,
	}
	engine := lyrics.NewEngine(lyricsCfg, nil)

	engine.GetCache().Set("Ария", "Штиль", &lyrics.Lyrics{
		Kind:         lyrics.KindLRC,
		SyncedLyrics: "[00:20.00] Штиль, ветер молчит",
		PlainLyrics:  "Штиль, ветер молчит",
		Source:       "yandex",
		CachedAt:     time.Now(),
	})

	u, _ := url.Parse("http://127.0.0.1:4533")
	server := proxy.NewServer(u, nil, nil, nil, true, engine)

	// Запрос /lyrics/api/get
	req := httptest.NewRequest(http.MethodGet, "/lyrics/api/get?artist_name=%D0%90%D1%80%D0%B8%D1%8F&track_name=%D0%A8%D1%82%D0%B8%D0%BB%D1%8C", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Ожидался статус 200 для /lyrics/api/get, получено: %d", rr.Code)
	}

	var dto lyrics.LRCLIBResponseDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &dto); err != nil {
		t.Fatalf("Ошибка разбора ответа LRCLIB: %v", err)
	}

	if dto.SyncedLyrics != "[00:20.00] Штиль, ветер молчит" {
		t.Errorf("Неверный SyncedLyrics: %s", dto.SyncedLyrics)
	}
}

func TestProxy_DiagnosticsLyricsAPI(t *testing.T) {
	lyricsCfg := &lyrics.Config{
		Enabled:  true,
		CacheTTL: 1 * time.Hour,
	}
	engine := lyrics.NewEngine(lyricsCfg, nil)
	dict := search.NewDictionary()
	searchEngine := search.NewEngine(dict)

	u, _ := url.Parse("http://127.0.0.1:4533")
	server := proxy.NewServer(u, searchEngine, dict, nil, true, engine)

	req := httptest.NewRequest(http.MethodGet, "/_smart/lyrics/sources", nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Ожидался статус 200 для /_smart/lyrics/sources, получено: %d", rr.Code)
	}

	if !strings.Contains(rr.Body.String(), "sources") {
		t.Errorf("В ответе sources не найден ключ: %s", rr.Body.String())
	}
}

func TestProxy_SubsonicLyrics_DetachedSearch_ClientDisconnect(t *testing.T) {
	lyricsCfg := &lyrics.Config{
		Enabled:        true,
		CacheTTL:       1 * time.Hour,
		RequestTimeout: 1 * time.Second,
	}
	engine := lyrics.NewEngine(lyricsCfg, nil)

	targetURL, _ := url.Parse("http://navidrome.mock:4533")
	revProxy := httputil.NewSingleHostReverseProxy(targetURL)
	revProxy.Transport = &mockNavidromeRoundTripper{authSuccess: true}
	interceptor := proxy.NewInterceptor(nil, nil, revProxy, engine)

	// Создаем запрос с контекстом, который отменяется немедленно
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/rest/getLyrics?u=admin&p=admin&artist=%D0%9A%D0%B8%D0%BD%D0%BE&title=%D0%9F%D0%B5%D1%87%D0%B0%D0%BB%D1%8C", nil).WithContext(ctx)
	rr := httptest.NewRecorder()

	cancel() // Имитация обрыва соединения клиентом

	interceptor.ServeHTTP(rr, req)

	// Сервер не должен паниковать при отключении клиента
	if rr.Code != http.StatusOK && rr.Code != 0 {
		t.Logf("Код ответа при отключенном клиенте: %d", rr.Code)
	}
}

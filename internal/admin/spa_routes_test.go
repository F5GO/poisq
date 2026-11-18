package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Адреса страниц Studio отдают index.html, а API и статические файлы — своё.
func TestStudioPageRoutesServeIndex(t *testing.T) {
	srv := NewAdminServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost:4535"+path, nil))
		return rec
	}
	for _, p := range []string{"/", "/library", "/lyrics/t-1", "/settings/keys", "/fixes"} {
		rec := get(p)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "poisq Studio") {
			t.Errorf("%s: HTTP %d, ждали страницу Studio", p, rec.Code)
		}
		if rec.Header().Get("Cache-Control") == "" {
			t.Errorf("%s: страница должна отдаваться без кэша", p)
		}
	}
	if rec := get("/app.js"); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<!DOCTYPE") {
		t.Errorf("/app.js: HTTP %d, ждали сам скрипт", rec.Code)
	}
	if rec := get("/missing.css"); rec.Code != http.StatusNotFound {
		t.Errorf("/missing.css: HTTP %d, ждали 404", rec.Code)
	}
	if rec := get("/api/v1/nope"); strings.Contains(rec.Body.String(), "poisq Studio") {
		t.Error("неизвестный API-адрес не должен отдавать страницу Studio")
	}
}

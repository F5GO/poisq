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
	for _, p := range []string{"/", "/library", "/metadata", "/lyrics/t-1", "/settings/keys", "/fixes"} {
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

func TestMetadataStudioAssetsExposeControls(t *testing.T) {
	index, err := embeddedUI.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, err := embeddedUI.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"panel-metadata", "metadata-policy-rules", "btn-metadata-audit", "metadata-recovery-status", "metadata-selection-count", "btn-metadata-select-visible"} {
		if !strings.Contains(string(index), want) {
			t.Errorf("index.html does not expose %q", want)
		}
	}
	for _, want := range []string{"/metadata/lock", "/metadata/policy", "/metadata/replaygain", "/metadata/artwork", "/metadata/similar", "/metadata/tags/preview", "/metadata/tags/apply", "/metadata/history?", "/metadata/history/revert/preview", "/metadata/history/revert/apply", "md-custom-tags", "md-rating", "loadStagingData(true)"} {
		if !strings.Contains(string(app), want) {
			t.Errorf("app.js does not use %q", want)
		}
	}
}

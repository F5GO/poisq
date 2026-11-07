package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func respFor(path, contentType, body string) *http.Response {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", contentType)
	rec.WriteString(body)
	r := rec.Result()
	r.Request = httptest.NewRequest("GET", path, nil)
	return r
}

func TestAdvertisePoisqExtensionJSON(t *testing.T) {
	r := respFor("/rest/getOpenSubsonicExtensions?f=json", "application/json",
		`{"subsonic-response":{"status":"ok","openSubsonic":true,"openSubsonicExtensions":[{"name":"formPost","versions":[1]}]}}`)
	if err := AdvertisePoisqExtension(r); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	s := string(b)
	if !strings.Contains(s, `"name":"poisq"`) || !strings.Contains(s, `"name":"formPost"`) {
		t.Fatalf("expected both extensions, got %s", s)
	}
}

func TestAdvertisePoisqExtensionIsIdempotent(t *testing.T) {
	r := respFor("/rest/getOpenSubsonicExtensions", "application/json",
		`{"subsonic-response":{"status":"ok","openSubsonicExtensions":[{"name":"poisq","versions":[1]}]}}`)
	_ = AdvertisePoisqExtension(r)
	b, _ := io.ReadAll(r.Body)
	if strings.Count(string(b), `"poisq"`) != 1 {
		t.Fatalf("poisq must not be duplicated: %s", b)
	}
}

func TestAdvertisePoisqExtensionXML(t *testing.T) {
	r := respFor("/rest/getOpenSubsonicExtensions.view", "text/xml",
		`<subsonic-response status="ok"><openSubsonicExtensions name="formPost"><versions>1</versions></openSubsonicExtensions></subsonic-response>`)
	_ = AdvertisePoisqExtension(r)
	b, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(b), `<openSubsonicExtensions name="poisq">`) {
		t.Fatalf("missing poisq: %s", b)
	}
}

func TestOtherPathsAreUntouched(t *testing.T) {
	const body = `{"subsonic-response":{"status":"ok"}}`
	r := respFor("/rest/ping", "application/json", body)
	_ = AdvertisePoisqExtension(r)
	b, _ := io.ReadAll(r.Body)
	if string(b) != body {
		t.Fatalf("ping must not change: %s", b)
	}
}

// Navidrome сжимает ответ, если клиент просит gzip (iOS просит всегда): прокси обязан всё равно объявить poisq.
func TestExtensionsAdvertisedWhenClientAcceptsGzip(t *testing.T) {
	nav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "" {
			w.Header().Set("Content-Encoding", "gzip") // имитация: сжатое тело прокси разобрать не сможет
			_, _ = w.Write([]byte("compressed"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok","openSubsonicExtensions":[]}}`))
	}))
	defer nav.Close()
	u, _ := url.Parse(nav.URL)
	srv := NewServer(u, nil, nil, nil, false)

	req := httptest.NewRequest(http.MethodGet, "/rest/getOpenSubsonicExtensions?f=json", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"name":"poisq"`) {
		t.Fatalf("poisq not advertised: %s", rec.Body.String())
	}
}

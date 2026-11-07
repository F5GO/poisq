package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
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

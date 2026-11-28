package proxy

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/F5GO/poisq/internal/security"
)

// rawGet отправляет путь как есть, без нормализации клиентом Go: так проверяются обходы фильтра.
func rawGet(t *testing.T, addr, path string) int {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: example.test\r\nConnection: close\r\n\r\n", path)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPublicEntryAllowsOnlySubsonicByDefault(t *testing.T) {
	var mu sync.Mutex
	var reached []string
	nav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reached = append(reached, r.URL.EscapedPath())
		mu.Unlock()
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok"}}`))
	}))
	defer nav.Close()
	u, _ := url.Parse(nav.URL)
	srv := NewServer(u, nil, nil, nil, false)
	access := &security.PublicAccess{}
	srv.SetPublicAccess(access)
	front := httptest.NewServer(srv)
	defer front.Close()
	addr := strings.TrimPrefix(front.URL, "http://")

	if code := rawGet(t, addr, "/rest/ping.view?u=a&t=b&s=c"); code != http.StatusOK {
		t.Fatalf("Subsonic request blocked: %d", code)
	}
	blocked := []string{
		"/", "/app/", "/app/index.html", "/api/song", "/auth/login", "/share/abc", "/metrics",
		"/REST/ping", "//app", "/rest/../app", "/rest/%2e%2e/app", "/rest%2Fping", "/%72est/ping",
		"/rest/ping%2F..%2F..%2Fapp", "/rest/a/b", "/rest/ping\\..\\app",
	}
	for _, p := range blocked {
		if code := rawGet(t, addr, p); code == http.StatusOK {
			t.Errorf("%s: got 200", p)
		}
	}
	mu.Lock()
	got := append([]string(nil), reached...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "/rest/ping.view" {
		t.Fatalf("Navidrome reached by %q, want only /rest/ping.view", got)
	}

	access.SetNavidromeWebOpen(true)
	if code := rawGet(t, addr, "/app/"); code != http.StatusOK {
		t.Fatalf("opened web interface still blocked: %d", code)
	}
}

func TestPublicEntryWithoutPolicyIsClosed(t *testing.T) {
	nav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Navidrome reached: %s", r.URL.Path)
	}))
	defer nav.Close()
	u, _ := url.Parse(nav.URL)
	srv := NewServer(u, nil, nil, nil, false)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/app/", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rr.Code)
	}
}

func TestSecureTransportPrefixIsServedByStudio(t *testing.T) {
	nav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Navidrome reached: %s", r.URL.Path)
	}))
	defer nav.Close()
	u, _ := url.Parse(nav.URL)
	srv := NewServer(u, nil, nil, nil, false)
	srv.SetPublicAccess(&security.PublicAccess{})

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/poisq-secure/v1/hello", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("without a handler: %d", rr.Code)
	}
	var hits int
	srv.SetSecureTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(http.StatusTeapot) }))
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/poisq-secure/v1/hello", nil))
	if rr.Code != http.StatusTeapot || hits != 1 {
		t.Fatalf("secure handler not used: %d", rr.Code)
	}
}

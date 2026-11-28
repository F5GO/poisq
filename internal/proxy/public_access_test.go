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

	"github.com/F5GO/poisq/internal/securetransport"
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

func TestSecureOnlyPolicyBlocksPlainSubsonic(t *testing.T) {
	var reached []string
	var bodies []string
	nav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		reached = append(reached, r.Form.Get("u"))
		bodies = append(bodies, r.Form.Get("x"))
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok"}}`))
	}))
	defer nav.Close()
	u, _ := url.Parse(nav.URL)
	srv := NewServer(u, nil, nil, nil, false)
	access := &security.PublicAccess{}
	access.SetSecureOnly(false, []string{"Alice"})
	srv.SetPublicAccess(access)

	do := func(req *http.Request) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	rr := do(httptest.NewRequest(http.MethodGet, "/rest/ping?u=alice&t=x&s=y&f=json", nil))
	if !strings.Contains(rr.Body.String(), `"code":50`) || !strings.Contains(rr.Header().Get("Content-Type"), "json") {
		t.Fatalf("JSON refusal: %s", rr.Body.String())
	}
	rr = do(httptest.NewRequest(http.MethodGet, "/rest/ping?u=ALICE", nil))
	if !strings.Contains(rr.Body.String(), `<error code="50"`) || !strings.Contains(rr.Header().Get("Content-Type"), "xml") {
		t.Fatalf("XML refusal: %s", rr.Body.String())
	}
	post := httptest.NewRequest(http.MethodPost, "/rest/ping", strings.NewReader("u=alice&t=x&s=y"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rr := do(post); !strings.Contains(rr.Body.String(), `code="50"`) {
		t.Fatalf("POST form refusal: %s", rr.Body.String())
	}
	if rr := do(httptest.NewRequest(http.MethodGet, "/rest/ping?apiKey=k", nil)); !strings.Contains(rr.Body.String(), `code="50"`) {
		t.Fatal("authorization without a username must be refused while accounts are flagged")
	}
	if len(reached) != 0 {
		t.Fatalf("refused requests reached Navidrome: %v", reached)
	}

	// Другой пользователь проходит, тело формы доходит до Navidrome целым.
	bob := httptest.NewRequest(http.MethodPost, "/rest/ping", strings.NewReader("u=bob&t=x&s=y&x=payload"))
	bob.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	do(bob)
	if len(reached) != 1 || reached[0] != "bob" || bodies[0] != "payload" {
		t.Fatalf("allowed POST: %v %v", reached, bodies)
	}

	// Запрос изнутри канала проходит даже для отмеченного пользователя.
	channel := httptest.NewRequest(http.MethodGet, "/rest/ping?u=alice&t=x&s=y", nil)
	do(channel.WithContext(securetransport.ChannelContext(channel.Context())))
	if len(reached) != 2 || reached[1] != "alice" {
		t.Fatalf("channel request was blocked: %v", reached)
	}

	access.SetSecureOnly(true, nil)
	do(httptest.NewRequest(http.MethodGet, "/rest/ping?u=carol", nil))
	if len(reached) != 2 {
		t.Fatal("installation-wide mode let a plain request through")
	}
}

func TestLANIngressIsTransparentAndCannotBeSpoofed(t *testing.T) {
	var reached []string
	nav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok"}}`))
	}))
	defer nav.Close()
	u, _ := url.Parse(nav.URL)
	srv := NewServer(u, nil, nil, nil, false)
	access := &security.PublicAccess{}
	access.SetSecureOnly(true, nil)
	srv.SetPublicAccess(access)
	public := httptest.NewServer(srv)
	defer public.Close()
	lan := httptest.NewServer(srv.LANHandler())
	defer lan.Close()

	get := func(base, path string, hdr map[string]string) int {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	spoof := map[string]string{"X-Forwarded-For": "192.168.1.5", "X-Real-IP": "127.0.0.1", "X-Poisq-LAN": "1", "Host": "localhost"}
	get(public.URL, "/app/", spoof)
	get(public.URL, "/rest/ping?u=alice&t=x&s=y", spoof)
	if len(reached) != 0 {
		t.Fatalf("public port let through web or plain Subsonic: %v", reached)
	}
	if code := get(lan.URL, "/app/", nil); code != http.StatusOK {
		t.Fatalf("LAN web interface: %d", code)
	}
	get(lan.URL, "/rest/ping?u=alice&t=x&s=y", nil)
	if len(reached) != 2 || reached[0] != "/app/" || reached[1] != "/rest/ping" {
		t.Fatalf("LAN port must be transparent: %v", reached)
	}
}

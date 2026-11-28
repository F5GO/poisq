package securetransport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/security"
)

type fakeVerifier struct {
	mu     sync.Mutex
	result map[string]VerifyResult // ключ — user\x00password
	down   bool
	calls  int
}

func (f *fakeVerifier) Verify(_ context.Context, user, password string) VerifyResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.down {
		return VerifyUnavailable
	}
	if r, ok := f.result[user+"\x00"+password]; ok {
		return r
	}
	return VerifyRejected
}

func (f *fakeVerifier) set(user, password string, r VerifyResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result[user+"\x00"+password] = r
}

type accountsFixture struct {
	srv      *Server
	inst     *Installation
	ts       *httptest.Server
	accounts *Accounts
	store    *DeviceStore
	verifier *fakeVerifier
	dbPath   string
}

func newAccountsFixture(t *testing.T) *accountsFixture {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "poisq.db")
	poisqDB, err := db.OpenPoisqDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { poisqDB.Close() })
	cipher, err := security.NewKeyCipher("test-secret", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store := NewDeviceStore(poisqDB, cipher)
	verifier := &fakeVerifier{result: map[string]VerifyResult{"alice\x00correct horse": VerifyOK}}
	accounts := NewAccounts(store, verifier)
	srv, inst, ts := newTestServer(t, func(_ context.Context, s *Session, req []byte, w io.Writer) error {
		user, _, _ := s.User()
		_, err := w.Write(append([]byte{StatusOK}, user...))
		return err
	})
	srv.SetAccounts(accounts)
	return &accountsFixture{srv: srv, inst: inst, ts: ts, accounts: accounts, store: store, verifier: verifier, dbPath: dbPath}
}

func loginRequest(user, password, device string) []byte {
	b := []byte{OpLogin, byte(len(user))}
	b = append(b, user...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(password)))
	b = append(b, password...)
	b = append(b, byte(len(device)))
	return append(b, device...)
}

// credential разбирает ответ входа: status · токен · ID устройства · срок.
func credential(t *testing.T, resp []byte) ([32]byte, string) {
	t.Helper()
	if len(resp) != 1+32+16+4 || resp[0] != StatusOK {
		t.Fatalf("credential response %x", resp)
	}
	if ttl := binary.BigEndian.Uint32(resp[49:]); ttl != uint32(AuthenticatedSessionTTL/time.Second) {
		t.Fatalf("ttl %d", ttl)
	}
	return [32]byte(resp[1:33]), hex.EncodeToString(resp[33:49])
}

func (f *accountsFixture) client(t *testing.T) *testClient {
	c := newTestClient(t, f.inst, f.ts.URL)
	if status := c.hello(); status != http.StatusOK {
		t.Fatalf("hello %d", status)
	}
	return c
}

func (f *accountsFixture) login(t *testing.T) (*testClient, [32]byte, string) {
	t.Helper()
	c := f.client(t)
	_, resp, err := c.call(loginRequest("alice", "correct horse", "iPhone Алисы"))
	if err != nil {
		t.Fatal(err)
	}
	token, id := credential(t, resp)
	return c, token, id
}

func TestLoginCreatesDeviceAndAuthenticatesSession(t *testing.T) {
	f := newAccountsFixture(t)
	c := f.client(t)
	if _, resp, _ := c.call([]byte{0x40}); !bytes.Equal(resp, []byte{StatusForbidden}) {
		t.Fatalf("app op before login: %x", resp)
	}
	if _, resp, _ := c.call(loginRequest("alice", "wrong", "x")); !bytes.Equal(resp, []byte{StatusUnauthorized}) {
		t.Fatalf("wrong password: %x", resp)
	}
	_, resp, _ := c.call(loginRequest("alice", "correct horse", "iPhone Алисы\x07"))
	_, id := credential(t, resp)
	if _, resp, _ := c.call([]byte{0x40}); !bytes.Equal(resp, append([]byte{StatusOK}, "alice"...)) {
		t.Fatalf("app op after login: %q", resp)
	}
	if _, resp, _ := c.call(loginRequest("alice", "correct horse", "x")); !bytes.Equal(resp, []byte{StatusForbidden}) {
		t.Fatalf("second login in the same session: %x", resp)
	}
	devices, err := f.accounts.Devices(context.Background())
	if err != nil || len(devices) != 1 || devices[0].ID != id || devices[0].Username != "alice" || devices[0].Name != "iPhone Алисы" {
		t.Fatalf("devices %+v, %v", devices, err)
	}
	f.srv.mu.Lock()
	sess := f.srv.sessions[c.session]
	f.srv.mu.Unlock()
	if left := time.Until(sess.expires); left < AuthenticatedSessionTTL-time.Minute {
		t.Fatalf("authenticated session expires in %v", left)
	}
}

func TestLoginStoresOnlyTokenHashAndEncryptedPassword(t *testing.T) {
	f := newAccountsFixture(t)
	_, token, _ := f.login(t)
	var hash []byte
	var enc string
	if err := f.store.db.QueryRow("SELECT token_hash, password_enc FROM secure_devices").Scan(&hash, &enc); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(hash, token[:]) || bytes.Contains([]byte(enc), []byte("correct horse")) || enc == "" {
		t.Fatal("token or password stored in clear")
	}
}

func TestLoginReportsUnavailableNavidromeAndBadInput(t *testing.T) {
	f := newAccountsFixture(t)
	c := f.client(t)
	f.verifier.down = true
	if _, resp, _ := c.call(loginRequest("alice", "correct horse", "x")); !bytes.Equal(resp, []byte{StatusUnavailable}) {
		t.Fatalf("Navidrome down: %x", resp)
	}
	f.verifier.down = false
	good := loginRequest("alice", "correct horse", "x")
	for name, body := range map[string][]byte{
		"empty":          {OpLogin},
		"trailing":       append(append([]byte(nil), good...), 0),
		"short":          good[:len(good)-1],
		"empty user":     loginRequest("", "p", "x"),
		"empty password": loginRequest("alice", "", "x"),
		"invalid utf8":   loginRequest("al\xffce", "p", "x"),
		"long user":      loginRequest(strings.Repeat("a", 200), "p", "x"),
	} {
		if _, resp, _ := c.call(body); !bytes.Equal(resp, []byte{StatusBadRequest}) {
			t.Errorf("%s: %x", name, resp)
		}
	}
	if devices, _ := f.accounts.Devices(context.Background()); len(devices) != 0 {
		t.Fatal("failed logins created devices")
	}
}

func TestResumeRotatesTokenWithGraceAndDetectsReuse(t *testing.T) {
	f := newAccountsFixture(t)
	_, first, id := f.login(t)

	c := f.client(t)
	_, resp, _ := c.call(append([]byte{OpResume}, first[:]...))
	second, gotID := credential(t, resp)
	if gotID != id || second == first {
		t.Fatal("resume must keep the device and rotate the token")
	}
	if _, resp, _ := c.call([]byte{0x40}); !bytes.Equal(resp, append([]byte{StatusOK}, "alice"...)) {
		t.Fatalf("resumed session is not authenticated: %q", resp)
	}

	// Ответ с новым токеном мог потеряться: прежний токен ещё принимается в окне повтора.
	retry := f.client(t)
	_, resp, _ = retry.call(append([]byte{OpResume}, first[:]...))
	third, _ := credential(t, resp)
	if _, resp, _ := f.client(t).call(append([]byte{OpResume}, second[:]...)); !bytes.Equal(resp, []byte{StatusRevoked}) {
		t.Fatalf("token issued before the retry must be invalid: %x", resp)
	}

	// После окна предъявление заменённого токена означает кражу: устройство отзывается вместе с сессиями.
	f.store.now = func() time.Time { return time.Now().Add(TokenReuseGrace + time.Minute) }
	if _, resp, _ := f.client(t).call(append([]byte{OpResume}, first[:]...)); !bytes.Equal(resp, []byte{StatusRevoked}) {
		t.Fatalf("reused token after grace: %x", resp)
	}
	if devices, _ := f.accounts.Devices(context.Background()); len(devices) != 0 {
		t.Fatal("device survived token reuse")
	}
	if status, _, _ := retry.call([]byte{0x40}); status != http.StatusGone {
		t.Fatalf("session of a revoked device still works: %d", status)
	}
	if _, resp, _ := f.client(t).call(append([]byte{OpResume}, third[:]...)); !bytes.Equal(resp, []byte{StatusRevoked}) {
		t.Fatalf("current token of a revoked device: %x", resp)
	}
}

func TestGraceWindowIsNotExtendedByRepeatedUse(t *testing.T) {
	f := newAccountsFixture(t)
	_, first, _ := f.login(t)
	start := time.Now()
	f.store.now = func() time.Time { return start }
	if _, resp, _ := f.client(t).call(append([]byte{OpResume}, first[:]...)); resp[0] != StatusOK {
		t.Fatal("resume failed")
	}
	f.store.now = func() time.Time { return start.Add(TokenReuseGrace - time.Second) }
	if _, resp, _ := f.client(t).call(append([]byte{OpResume}, first[:]...)); resp[0] != StatusOK {
		t.Fatal("previous token rejected inside the grace window")
	}
	f.store.now = func() time.Time { return start.Add(TokenReuseGrace + time.Second) }
	if _, resp, _ := f.client(t).call(append([]byte{OpResume}, first[:]...)); !bytes.Equal(resp, []byte{StatusRevoked}) {
		t.Fatalf("grace window was extended by repeated use: %x", resp)
	}
}

func TestResumeRevokesAllDevicesWhenPasswordChanged(t *testing.T) {
	f := newAccountsFixture(t)
	phone, token, _ := f.login(t)
	tablet, _, _ := f.login(t)
	f.verifier.set("alice", "correct horse", VerifyRejected) // пароль сменили в Navidrome
	if _, resp, _ := f.client(t).call(append([]byte{OpResume}, token[:]...)); !bytes.Equal(resp, []byte{StatusRevoked}) {
		t.Fatalf("resume after password change: %x", resp)
	}
	if devices, _ := f.accounts.Devices(context.Background()); len(devices) != 0 {
		t.Fatalf("devices left after password change: %d", len(devices))
	}
	for _, c := range []*testClient{phone, tablet} {
		if status, _, _ := c.call([]byte{0x40}); status != http.StatusGone {
			t.Fatalf("session survived password change: %d", status)
		}
	}
}

func TestResumeWithNavidromeDownKeepsToken(t *testing.T) {
	f := newAccountsFixture(t)
	_, token, _ := f.login(t)
	f.verifier.down = true
	if _, resp, _ := f.client(t).call(append([]byte{OpResume}, token[:]...)); !bytes.Equal(resp, []byte{StatusUnavailable}) {
		t.Fatalf("resume with Navidrome down: %x", resp)
	}
	f.verifier.down = false
	f.store.now = func() time.Time { return time.Now().Add(time.Hour) } // далеко за окном повтора
	if _, resp, _ := f.client(t).call(append([]byte{OpResume}, token[:]...)); resp[0] != StatusOK {
		t.Fatalf("token must stay valid when Navidrome was unavailable: %x", resp)
	}
}

func TestSignOutAndStudioRevokeEndSessions(t *testing.T) {
	f := newAccountsFixture(t)
	c, token, id := f.login(t)
	other := f.client(t)
	_, resp, _ := other.call(append([]byte{OpResume}, token[:]...))
	credential(t, resp)
	if _, resp, _ := c.call([]byte{OpSignOut}); !bytes.Equal(resp, []byte{StatusOK}) {
		t.Fatalf("sign out: %x", resp)
	}
	if status, _, _ := other.call([]byte{0x40}); status != http.StatusGone {
		t.Fatalf("other session of the signed-out device: %d", status)
	}

	c2, _, id2 := f.login(t)
	if id2 == id {
		t.Fatal("new login reused a revoked device ID")
	}
	if ok, err := f.accounts.Revoke(context.Background(), id2); !ok || err != nil {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if status, _, _ := c2.call([]byte{0x40}); status != http.StatusGone {
		t.Fatalf("session after Studio revoke: %d", status)
	}
	if ok, _ := f.accounts.Revoke(context.Background(), id2); ok {
		t.Fatal("revoked twice")
	}
	if _, resp, _ := f.client(t).call([]byte{OpSignOut}); !bytes.Equal(resp, []byte{StatusForbidden}) {
		t.Fatalf("sign out before login: %x", resp)
	}
}

func TestLoginLimits(t *testing.T) {
	f := newAccountsFixture(t)
	c := f.client(t)
	limited := 0
	for range loginBurstUser + 3 {
		if _, resp, _ := c.call(loginRequest("Bob", "guess", "x")); bytes.Equal(resp, []byte{StatusRateLimited}) {
			limited++
		}
	}
	if limited != 3 {
		t.Fatalf("rate limited %d attempts, want 3", limited)
	}
	if _, resp, _ := c.call(loginRequest("bob", "guess", "x")); !bytes.Equal(resp, []byte{StatusRateLimited}) {
		t.Fatal("limit must not depend on username case")
	}

	for range MaxDevicesPerUser {
		if _, _, err := f.store.Create(context.Background(), "carol", "d", "pw"); err != nil {
			t.Fatal(err)
		}
	}
	f.verifier.set("carol", "pw", VerifyOK)
	if _, resp, _ := f.client(t).call(loginRequest("carol", "pw", "x")); !bytes.Equal(resp, []byte{StatusTooManyDevices}) {
		t.Fatalf("too many devices: %x", resp)
	}
}

func TestSessionsPerDeviceAreCapped(t *testing.T) {
	f := newAccountsFixture(t)
	_, token, _ := f.login(t)
	var clients []*testClient
	for range MaxSessionsPerDevice + 2 {
		c := f.client(t)
		_, resp, _ := c.call(append([]byte{OpResume}, token[:]...))
		token, _ = credential(t, resp)
		clients = append(clients, c)
	}
	alive := 0
	for _, c := range clients {
		if status, _, _ := c.call([]byte{0x40}); status == http.StatusOK {
			alive++
		}
	}
	if alive != MaxSessionsPerDevice {
		t.Fatalf("%d sessions alive, want %d", alive, MaxSessionsPerDevice)
	}
}

func TestNavidromeVerifier(t *testing.T) {
	var lastQuery url.Values
	nav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.Query()
		switch r.URL.Query().Get("u") {
		case "ok":
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok"}}`))
		case "wrong":
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"failed","error":{"code":40}}}`))
		case "other":
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"failed","error":{"code":0}}}`))
		case "redirect":
			http.Redirect(w, r, "https://example.com/", http.StatusFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer nav.Close()
	base, _ := url.Parse(nav.URL)
	v := NewNavidromeVerifier(base, nil)
	for user, want := range map[string]VerifyResult{"ok": VerifyOK, "wrong": VerifyRejected, "other": VerifyUnavailable, "redirect": VerifyUnavailable, "boom": VerifyUnavailable} {
		if got := v.Verify(context.Background(), user, "secret"); got != want {
			t.Errorf("%s: %v, want %v", user, got, want)
		}
	}
	if lastQuery.Get("p") != "" || strings.Contains(lastQuery.Encode(), "secret") || lastQuery.Get("t") == "" || lastQuery.Get("s") == "" {
		t.Fatalf("password must travel only as a salted token: %v", lastQuery)
	}
	nav.Close()
	if got := v.Verify(context.Background(), "ok", "secret"); got != VerifyUnavailable {
		t.Fatalf("Navidrome down: %v", got)
	}
}

func TestCleanDeviceName(t *testing.T) {
	for in, want := range map[string]string{
		"  iPhone  ":            "iPhone",
		"a\x00b\nc‮":            "abc",
		"":                      "Устройство",
		strings.Repeat("я", 40): strings.Repeat("я", 32),
	} {
		if got := CleanDeviceName(in); got != want {
			t.Errorf("CleanDeviceName(%q) = %q, want %q", in, got, want)
		}
	}
}

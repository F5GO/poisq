package securetransport

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testClient — эталонный клиент протокола на Go; Swift-клиент обязан вести себя так же.
type testClient struct {
	t       *testing.T
	base    string
	keys    *KeyStore
	epoch   uint32
	session [16]byte
	c2s     [32]byte
	s2c     [32]byte
	counter uint64
}

func newTestServer(t *testing.T, handler Handler) (*Server, *Installation, *httptest.Server) {
	t.Helper()
	inst := NewInstallation(t.TempDir())
	if err := inst.Configure(true, "https://music.example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	if handler == nil {
		handler = PingHandler
	}
	srv := NewServer(inst, handler, func(r *http.Request) string { return r.Header.Get("X-Test-IP") })
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, inst, ts
}

func newTestClient(t *testing.T, inst *Installation, base string) *testClient {
	t.Helper()
	_, _, keys := inst.State()
	resp, err := http.Get(base + PathPrefix + "certificate")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	cert, err := ParseWorkingCertificate(raw, keys.RootPublicKey(), keys.InstallationID())
	if err != nil {
		t.Fatalf("certificate endpoint: %v", err)
	}
	return &testClient{t: t, base: base, keys: keys, epoch: cert.Epoch}
}

func post(t *testing.T, url string, body []byte, ip string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if ip != "" {
		req.Header.Set("X-Test-IP", ip)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (c *testClient) helloMessage(epoch uint32) (*nkInitiator, []byte) {
	c.t.Helper()
	cert, err := ParseWorkingCertificate(c.keys.CurrentCertificate(), c.keys.RootPublicKey(), c.keys.InstallationID())
	if err != nil {
		c.t.Fatal(err)
	}
	rs, _ := ecdh.X25519().NewPublicKey(cert.WorkingKey[:])
	init, msg1, err := newNKInitiator(handshakePrologue(c.keys.InstallationID(), epoch), rs, nil, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	body := binary.BigEndian.AppendUint32([]byte{wireVersion}, epoch)
	return init, append(body, msg1...)
}

func (c *testClient) hello() int {
	c.t.Helper()
	init, body := c.helloMessage(c.epoch)
	status, msg2 := post(c.t, c.base+PathPrefix+"hello", body, "")
	if status != http.StatusOK {
		return status
	}
	res, err := init.readMessage2(msg2)
	if err != nil || len(res.payload) != helloPayloadSize {
		c.t.Fatalf("hello response: %v", err)
	}
	copy(c.session[:], res.payload[:16])
	c.c2s, c.s2c, c.counter = res.initiatorToResponder, res.responderToInitiator, 0
	if ttl := binary.BigEndian.Uint32(res.payload[16:20]); ttl != uint32(PreAuthSessionTTL/time.Second) {
		c.t.Fatalf("ttl %d", ttl)
	}
	return status
}

func (c *testClient) seal(counter uint64, request []byte) []byte {
	header := callHeader(c.session, counter)
	ct, err := sealAESGCM(deriveKey(c.c2s, requestLabel, counter), 0, recordAAD(header, 0, true), request)
	if err != nil {
		c.t.Fatal(err)
	}
	return append(header, ct...)
}

func (c *testClient) send(counter uint64, body []byte) (int, []byte, error) {
	status, out := post(c.t, c.base+PathPrefix+"call", body, "")
	if status != http.StatusOK {
		return status, nil, nil
	}
	plain, err := readRecords(bytes.NewReader(out), deriveKey(c.s2c, responseLabel, counter), body[:callHeaderSize], 64<<20)
	return status, plain, err
}

func (c *testClient) call(request []byte) (int, []byte, error) {
	c.counter++
	return c.send(c.counter, c.seal(c.counter, request))
}

func TestChannelPing(t *testing.T) {
	_, inst, ts := newTestServer(t, nil)
	c := newTestClient(t, inst, ts.URL)
	if status := c.hello(); status != http.StatusOK {
		t.Fatalf("hello: %d", status)
	}
	status, resp, err := c.call(append([]byte{OpPing}, "проверка"...))
	if status != http.StatusOK || err != nil || !bytes.Equal(resp, append([]byte{StatusOK}, "проверка"...)) {
		t.Fatalf("ping: %d %q %v", status, resp, err)
	}
	// До входа любые операции, кроме проверки и входа, запрещены.
	if _, resp, _ := c.call([]byte{0x7f}); !bytes.Equal(resp, []byte{StatusForbidden}) {
		t.Fatalf("op before sign-in: %q", resp)
	}
}

func TestChannelRejectsReplayButAllowsReordering(t *testing.T) {
	_, inst, ts := newTestServer(t, nil)
	c := newTestClient(t, inst, ts.URL)
	c.hello()
	first := c.seal(1, []byte{OpPing})
	second := c.seal(2, []byte{OpPing})
	if status, _, err := c.send(2, second); status != http.StatusOK || err != nil {
		t.Fatalf("counter 2 first: %d %v", status, err)
	}
	if status, _, err := c.send(1, first); status != http.StatusOK || err != nil {
		t.Fatalf("out-of-order counter 1: %d %v", status, err)
	}
	if status, _, _ := c.send(1, first); status != http.StatusBadRequest {
		t.Fatalf("replay accepted: %d", status)
	}
	if status, _, _ := c.send(0, c.seal(0, []byte{OpPing})); status != http.StatusBadRequest {
		t.Fatalf("counter 0 accepted: %d", status)
	}
}

func TestChannelTamperingDoesNotBurnCounter(t *testing.T) {
	_, inst, ts := newTestServer(t, nil)
	c := newTestClient(t, inst, ts.URL)
	c.hello()
	good := c.seal(5, []byte{OpPing, 1, 2, 3})
	for _, i := range []int{0, 1, 20, callHeaderSize, len(good) - 1} {
		bad := append([]byte(nil), good...)
		bad[i] ^= 1
		if status, _, _ := c.send(5, bad); status == http.StatusOK {
			t.Fatalf("tampered byte %d accepted", i)
		}
	}
	if status, _, err := c.send(5, good); status != http.StatusOK || err != nil {
		t.Fatalf("genuine request after tampering: %d %v", status, err)
	}
	// Запрос другой сессии не расшифровывается ключами этой.
	other := newTestClient(t, inst, ts.URL)
	other.hello()
	forged := other.seal(6, []byte{OpPing})
	copy(forged[1:17], c.session[:])
	if status, _, _ := c.send(6, forged); status != http.StatusBadRequest {
		t.Fatalf("cross-session request accepted: %d", status)
	}
}

func TestChannelSessionLifecycle(t *testing.T) {
	srv, inst, ts := newTestServer(t, nil)
	c := newTestClient(t, inst, ts.URL)
	c.hello()
	unknown := *c
	unknown.session = [16]byte{1}
	if status, _, _ := unknown.call([]byte{OpPing}); status != http.StatusGone {
		t.Fatalf("unknown session: %d", status)
	}
	srv.now = func() time.Time { return time.Now().Add(PreAuthSessionTTL + time.Second) }
	if status, _, _ := c.call([]byte{OpPing}); status != http.StatusGone {
		t.Fatalf("expired session: %d", status)
	}
	srv.now = time.Now
	c.hello()
	if err := inst.Configure(false, "https://music.example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := c.call([]byte{OpPing}); status != http.StatusNotFound {
		t.Fatalf("disabled: %d", status)
	}
	if err := inst.Configure(true, "https://music.example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := c.call([]byte{OpPing}); status != http.StatusGone {
		t.Fatalf("session survived disabling: %d", status)
	}
}

func TestHelloRejectsBadInput(t *testing.T) {
	_, inst, ts := newTestServer(t, nil)
	c := newTestClient(t, inst, ts.URL)

	status, body := post(t, ts.URL+PathPrefix+"hello", func() []byte { _, b := c.helloMessage(c.epoch + 5); return b }(), "")
	if status != http.StatusConflict {
		t.Fatalf("unknown epoch: %d", status)
	}
	if _, err := ParseWorkingCertificate(body, c.keys.RootPublicKey(), c.keys.InstallationID()); err != nil {
		t.Fatalf("409 must carry the current certificate: %v", err)
	}
	_, good := c.helloMessage(c.epoch)
	for name, body := range map[string][]byte{
		"empty":    nil,
		"short":    good[:helloSize-1],
		"long":     append(append([]byte(nil), good...), 0),
		"version":  append([]byte{2}, good[1:]...),
		"tampered": append(append([]byte(nil), good[:helloSize-1]...), good[helloSize-1]^1),
	} {
		if status, _ := post(t, ts.URL+PathPrefix+"hello", body, ""); status == http.StatusOK {
			t.Errorf("%s: accepted", name)
		}
	}
	// Первое сообщение с полезной нагрузкой отвергается: сервер ничего не принимает до подтверждения канала.
	cert, _ := ParseWorkingCertificate(c.keys.CurrentCertificate(), c.keys.RootPublicKey(), c.keys.InstallationID())
	rs, _ := ecdh.X25519().NewPublicKey(cert.WorkingKey[:])
	_, msg1, _ := newNKInitiator(handshakePrologue(c.keys.InstallationID(), c.epoch), rs, nil, []byte("password"))
	withPayload := append(binary.BigEndian.AppendUint32([]byte{wireVersion}, c.epoch), msg1...)
	if status, _ := post(t, ts.URL+PathPrefix+"hello", withPayload, ""); status == http.StatusOK {
		t.Fatal("hello with payload accepted")
	}
	if resp, err := http.Get(ts.URL + PathPrefix + "hello"); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET hello: %v %v", resp.StatusCode, err)
	}
	if resp, err := http.Get(ts.URL + PathPrefix + "other"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path: %v %v", resp.StatusCode, err)
	}
}

func TestCallRejectsOversizedBody(t *testing.T) {
	_, inst, ts := newTestServer(t, nil)
	c := newTestClient(t, inst, ts.URL)
	c.hello()
	if status, _, _ := c.call(make([]byte, MaxRequestSize+1)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request: %d", status)
	}
	if status, _, err := c.call(append([]byte{OpPing}, make([]byte, MaxRequestSize-1)...)); status != http.StatusOK || err != nil {
		t.Fatalf("maximum request: %d %v", status, err)
	}
}

func TestHelloRateLimitAndSessionCap(t *testing.T) {
	srv, inst, ts := newTestServer(t, nil)
	c := newTestClient(t, inst, ts.URL)
	limited := 0
	for range helloBurst + 5 {
		_, body := c.helloMessage(c.epoch)
		if status, _ := post(t, ts.URL+PathPrefix+"hello", body, "203.0.113.7"); status == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 5 {
		t.Fatalf("rate limited %d requests, want 5", limited)
	}
	_, body := c.helloMessage(c.epoch)
	if status, _ := post(t, ts.URL+PathPrefix+"hello", body, "203.0.113.8"); status != http.StatusOK {
		t.Fatalf("another address was limited: %d", status)
	}

	srv.mu.Lock()
	for i := range MaxSessions {
		var id [16]byte
		binary.BigEndian.PutUint32(id[:], uint32(i)+1<<20)
		srv.sessions[id] = &Session{id: id, expires: time.Now().Add(time.Hour)}
	}
	srv.mu.Unlock()
	_, body = c.helloMessage(c.epoch)
	if status, _ := post(t, ts.URL+PathPrefix+"hello", body, "203.0.113.9"); status != http.StatusServiceUnavailable {
		t.Fatalf("full session table: %d", status)
	}
}

func TestResponseRecords(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), MaxRecordSize/4+3) // ~4,2 записи
	var failAfterStart atomic.Bool
	handler := func(_ context.Context, _ *Session, req []byte, w io.Writer) error {
		if _, err := w.Write(big); err != nil {
			return err
		}
		if failAfterStart.Load() {
			return errors.New("upstream failed")
		}
		return nil
	}
	srv, inst, ts := newTestServer(t, handler)
	c := newTestClient(t, inst, ts.URL)
	c.hello()
	const appOp = 0x40
	if _, resp, _ := c.call([]byte{appOp}); !bytes.Equal(resp, []byte{StatusForbidden}) {
		t.Fatalf("application op before sign-in: %q", resp)
	}
	srv.mu.Lock()
	sess := srv.sessions[c.session]
	srv.mu.Unlock()
	srv.authenticate(sess, Device{ID: "00112233445566778899aabbccddeeff", Username: "alice"}, "pw", time.Now())
	if status, resp, err := c.call([]byte{appOp}); status != http.StatusOK || err != nil || !bytes.Equal(resp, big) {
		t.Fatalf("large response: %d %d %v", status, len(resp), err)
	}
	failAfterStart.Store(true)
	if _, _, err := c.call([]byte{appOp}); err == nil {
		t.Fatal("response without the final record was accepted")
	}
}

func TestReadRecordsRejectsManipulation(t *testing.T) {
	key, header := bytes.Repeat([]byte{7}, 32), callHeader([16]byte{1}, 9)
	var buf bytes.Buffer
	rw := newRecordWriter(&buf, nil, key, header)
	payload := bytes.Repeat([]byte{1}, MaxRecordSize+10)
	_, _ = rw.Write(payload)
	_ = rw.Close()
	raw := buf.Bytes()
	if out, err := readRecords(bytes.NewReader(raw), key, header, len(payload)); err != nil || !bytes.Equal(out, payload) {
		t.Fatalf("roundtrip: %v", err)
	}
	firstLen := 4 + int(binary.BigEndian.Uint32(raw[:4])&^finalRecordBit)
	first, second := raw[:firstLen], raw[firstLen:]
	cases := map[string][]byte{
		"truncated":       raw[:len(raw)-1],
		"no final record": first,
		"reordered":       append(append([]byte(nil), second...), first...),
		"flag cleared":    append(append([]byte(nil), first...), append([]byte{second[0] &^ 0x80}, second[1:]...)...),
		"flag set early":  append(append([]byte{first[0] | 0x80}, first[1:]...), second...),
	}
	for name, data := range cases {
		if _, err := readRecords(bytes.NewReader(data), key, header, len(payload)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := readRecords(bytes.NewReader(raw), key, callHeader([16]byte{1}, 10), len(payload)); err == nil {
		t.Error("response bound to another request accepted")
	}
	if _, err := readRecords(bytes.NewReader(raw), key, header, len(payload)-1); err == nil {
		t.Error("limit not enforced")
	}
	if _, err := readRecords(strings.NewReader("\x7f\xff\xff\xff"), key, header, 1<<30); err == nil {
		t.Error("oversized frame accepted")
	}
}

func TestReplayWindow(t *testing.T) {
	var w replayWindow
	for _, tc := range []struct {
		c    uint64
		want bool
	}{
		{0, false}, {1, true}, {1, false}, {3, true}, {2, true}, {2, false},
		{5000, true}, {5000 - replayWindowSize, false}, {5000 - replayWindowSize + 1, true}, {4999, true},
		{20000, true}, {4999, false}, {19999, true}, {20000 - replayWindowSize + 1, true},
	} {
		if got := w.accept(tc.c); got != tc.want {
			t.Fatalf("accept(%d) = %v, want %v", tc.c, got, tc.want)
		}
	}
}

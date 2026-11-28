package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/securetransport"
	"github.com/F5GO/poisq/internal/security"
	"rsc.io/qr"
)

func TestSecureTransportSettings(t *testing.T) {
	tmp := t.TempDir()
	poisqDB, err := db.OpenPoisqDatabase(filepath.Join(tmp, "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer poisqDB.Close()
	keysDir := filepath.Join(tmp, "secure-transport")
	inst := securetransport.NewInstallation(keysDir)
	if err := LoadSecureTransport(poisqDB, inst, time.Now()); err != nil {
		t.Fatal(err)
	}
	srv := &AdminServer{poisqDB: poisqDB}
	srv.SetSecureTransport(inst)

	call := func(method, body string) (int, secureTransportStatus) {
		rr := httptest.NewRecorder()
		srv.handleSecureTransport(rr, httptest.NewRequest(method, "/api/v1/secure-transport", strings.NewReader(body)))
		var st secureTransportStatus
		_ = json.Unmarshal(rr.Body.Bytes(), &st)
		return rr.Code, st
	}
	qrStatus := func() (int, string, string) {
		rr := httptest.NewRecorder()
		srv.handleSecureTransportQR(rr, httptest.NewRequest(http.MethodGet, "/api/v1/secure-transport/qr.svg", nil))
		return rr.Code, rr.Header().Get("Content-Type"), rr.Body.String()
	}

	if code, st := call(http.MethodGet, ""); code != http.StatusOK || st.Enabled || st.KeysCreated || st.PairingCode != "" {
		t.Fatalf("default state: %d %+v", code, st)
	}
	if code, _, _ := qrStatus(); code != http.StatusNotFound {
		t.Fatalf("QR served while disabled: %d", code)
	}
	for _, body := range []string{`{"enabled":true}`, `{"enabled":true,"publicOrigin":"http://music.example.com"}`, `{"enabled":true,"publicOrigin":"https://music.example.com/path"}`, `nope`} {
		if code, _ := call(http.MethodPost, body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, code)
		}
	}
	if _, _, keys := inst.State(); keys != nil {
		t.Fatal("rejected settings created keys")
	}

	code, st := call(http.MethodPost, `{"enabled":true,"publicOrigin":"https://Music.Example.com:443/"}`)
	if code != http.StatusOK || !st.Enabled || st.PublicOrigin != "https://music.example.com" || !st.KeysCreated ||
		st.Fingerprint == "" || st.CurrentEpoch != 1 || !strings.HasPrefix(st.PairingCode, "poisq-secure:v1:"+st.InstallationID+":") {
		t.Fatalf("enable: %d %+v", code, st)
	}
	qrCode, contentType, svg := qrStatus()
	if qrCode != http.StatusOK || contentType != "image/svg+xml" || !strings.HasPrefix(svg, "<svg") || strings.Contains(svg, "poisq-secure") {
		t.Fatalf("QR: %d %q", qrCode, contentType)
	}

	reloaded := securetransport.NewInstallation(keysDir)
	if err := LoadSecureTransport(poisqDB, reloaded, time.Now()); err != nil {
		t.Fatal(err)
	}
	if enabled, origin, keys := reloaded.State(); !enabled || origin != st.PublicOrigin || keys == nil ||
		securetransport.KeyFingerprint(keys.RootPublicKey()) != st.Fingerprint {
		t.Fatal("settings or keys were not persisted")
	}

	code, off := call(http.MethodPost, `{"enabled":false,"publicOrigin":"https://music.example.com"}`)
	if code != http.StatusOK || off.Enabled || off.PairingCode != "" || off.Fingerprint != st.Fingerprint {
		t.Fatalf("disable must keep the same keys and hide the QR: %d %+v", code, off)
	}
	if code, _, _ := qrStatus(); code != http.StatusNotFound {
		t.Fatalf("QR served after disabling: %d", code)
	}

	if err := db.SetSetting(poisqDB, secureTransportSettingKey, `{"enabled":true,"publicOrigin":"ftp://x"}`); err != nil {
		t.Fatal(err)
	}
	broken := securetransport.NewInstallation(keysDir)
	if err := LoadSecureTransport(poisqDB, broken, time.Now()); err == nil {
		t.Fatal("invalid stored settings were accepted")
	}
	if enabled, _, _ := broken.State(); enabled {
		t.Fatal("invalid stored settings must leave Secure Transport disabled")
	}
}

func TestQRSVGDrawsEveryModule(t *testing.T) {
	const text = "poisq-secure:v1:00112233445566778899aabbccddeeff:ebVWLo_mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ:aHR0cHM6Ly9tdXNpYy5leGFtcGxlLmNvbTo4NDQz"
	svg, err := qrSVG(text)
	if err != nil {
		t.Fatal(err)
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	black := 0
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				black++
			}
		}
	}
	if got := strings.Count(svg, "h1v1h-1z"); got != black || black == 0 {
		t.Fatalf("drew %d modules, want %d", got, black)
	}
	if want := `viewBox="0 0 ` + itoa(code.Size+8) + " " + itoa(code.Size+8) + `"`; !strings.Contains(svg, want) {
		t.Fatalf("missing quiet zone: %s", want)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

type allowVerifier struct{}

func (allowVerifier) Verify(context.Context, string, string) securetransport.VerifyResult {
	return securetransport.VerifyOK
}

func TestSecureDevicesAPI(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "poisq.db")
	poisqDB, err := db.OpenPoisqDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer poisqDB.Close()
	cipher, err := security.NewKeyCipher("test", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store := securetransport.NewDeviceStore(poisqDB, cipher)
	accounts := securetransport.NewAccounts(store, allowVerifier{})
	securetransport.NewServer(securetransport.NewInstallation(tmp), securetransport.PingHandler, func(*http.Request) string { return "" }).SetAccounts(accounts)
	srv := &AdminServer{}
	srv.SetSecureAccounts(accounts)

	call := func(method, body string) (int, []securetransport.Device) {
		rr := httptest.NewRecorder()
		srv.handleSecureDevices(rr, httptest.NewRequest(method, "/api/v1/secure-transport/devices", strings.NewReader(body)))
		var out struct {
			Devices []securetransport.Device `json:"devices"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out.Devices
	}
	if code, devices := call(http.MethodGet, ""); code != http.StatusOK || devices == nil || len(devices) != 0 {
		t.Fatalf("empty list: %d %v", code, devices)
	}
	d, _, err := store.Create(context.Background(), "alice", "iPhone", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if code, devices := call(http.MethodGet, ""); code != http.StatusOK || len(devices) != 1 || devices[0].Name != "iPhone" {
		t.Fatalf("list: %d %+v", code, devices)
	}
	for _, body := range []string{`{"id":"short"}`, `nope`, `{"id":"` + strings.Repeat("a", 40) + `"}`} {
		if code, _ := call(http.MethodPost, body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, code)
		}
	}
	if code, devices := call(http.MethodPost, `{"id":"`+d.ID+`"}`); code != http.StatusOK || len(devices) != 0 {
		t.Fatalf("revoke: %d %+v", code, devices)
	}
	if code, _ := call(http.MethodPost, `{"id":"`+d.ID+`"}`); code != http.StatusNotFound {
		t.Fatalf("second revoke: %d", code)
	}
}

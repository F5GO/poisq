package admin

import (
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
)

func TestPublicAccessSettings(t *testing.T) {
	poisqDB, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer poisqDB.Close()

	access := &security.PublicAccess{}
	if err := LoadPublicAccess(poisqDB, access); err != nil || access.NavidromeWebOpen() {
		t.Fatalf("default must be closed: open=%v err=%v", access.NavidromeWebOpen(), err)
	}
	srv := &AdminServer{poisqDB: poisqDB}
	srv.SetPublicAccess(access)

	call := func(method, body string) (int, publicAccessSettings) {
		rr := httptest.NewRecorder()
		srv.handlePublicAccess(rr, httptest.NewRequest(method, "/api/v1/public-access", strings.NewReader(body)))
		var st publicAccessSettings
		_ = json.Unmarshal(rr.Body.Bytes(), &st)
		return rr.Code, st
	}
	if code, st := call(http.MethodGet, ""); code != http.StatusOK || st.NavidromeWebOpen {
		t.Fatalf("GET: %d %+v", code, st)
	}
	if code, st := call(http.MethodPost, `{"navidromeWebOpen":true}`); code != http.StatusOK || !st.NavidromeWebOpen || !access.NavidromeWebOpen() {
		t.Fatalf("POST open: %d %+v", code, st)
	}
	reloaded := &security.PublicAccess{}
	if err := LoadPublicAccess(poisqDB, reloaded); err != nil || !reloaded.NavidromeWebOpen() {
		t.Fatalf("open setting not persisted: %v", err)
	}
	if code, _ := call(http.MethodPost, `not json`); code != http.StatusBadRequest || !access.NavidromeWebOpen() {
		t.Fatalf("bad body changed policy or wrong code: %d", code)
	}
	if code, _ := call(http.MethodPost, `{"navidromeWebOpen":false,"pad":"`+strings.Repeat("x", 300<<10)+`"}`); code != http.StatusBadRequest {
		t.Fatalf("oversized body accepted: %d", code)
	}
	if code, _ := call(http.MethodDelete, ""); code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE: %d", code)
	}

	if err := db.SetSetting(poisqDB, publicAccessSettingKey, "{broken"); err != nil {
		t.Fatal(err)
	}
	if err := LoadPublicAccess(poisqDB, reloaded); err == nil || reloaded.NavidromeWebOpen() {
		t.Fatal("corrupt setting must keep web closed and report an error")
	}
	if err := LoadPublicAccess(nil, reloaded); err == nil || reloaded.NavidromeWebOpen() {
		t.Fatal("missing database must keep web closed")
	}
}

func TestSecureOnlyPolicySettings(t *testing.T) {
	tmp := t.TempDir()
	poisqDB, err := db.OpenPoisqDatabase(filepath.Join(tmp, "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer poisqDB.Close()
	access := &security.PublicAccess{}
	inst := securetransport.NewInstallation(filepath.Join(tmp, "keys"))
	srv := &AdminServer{poisqDB: poisqDB}
	srv.SetPublicAccess(access)
	srv.SetSecureTransport(inst)
	call := func(body string) (int, publicAccessSettings) {
		rr := httptest.NewRecorder()
		srv.handlePublicAccess(rr, httptest.NewRequest(http.MethodPost, "/api/v1/public-access", strings.NewReader(body)))
		var st publicAccessSettings
		_ = json.Unmarshal(rr.Body.Bytes(), &st)
		return rr.Code, st
	}
	if code, _ := call(`{"secureOnlyUsers":["alice"]}`); code != http.StatusBadRequest {
		t.Fatalf("policy without Secure Transport: %d", code)
	}
	if err := inst.Configure(true, "https://music.example.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	code, st := call(`{"secureOnlyUsers":[" Alice ","alice","",  "BOB"]}`)
	if code != http.StatusOK || strings.Join(st.SecureOnlyUsers, ",") != "alice,bob" || st.SecureOnly || st.NavidromeWebOpen {
		t.Fatalf("users: %d %+v", code, st)
	}
	// Переключатель веб-интерфейса не сбрасывает политику.
	if code, st := call(`{"navidromeWebOpen":true}`); code != http.StatusOK || len(st.SecureOnlyUsers) != 2 || !st.NavidromeWebOpen {
		t.Fatalf("partial update: %d %+v", code, st)
	}
	if access.PlainSubsonicAllowed("ALICE") || !access.PlainSubsonicAllowed("carol") || access.PlainSubsonicAllowed("") {
		t.Fatal("policy not applied")
	}
	if code, st := call(`{"secureOnly":true}`); code != http.StatusOK || !st.SecureOnly || access.PlainSubsonicAllowed("carol") {
		t.Fatalf("installation mode: %d %+v", code, st)
	}
	reloaded := &security.PublicAccess{}
	if err := LoadPublicAccess(poisqDB, reloaded); err != nil || reloaded.PlainSubsonicAllowed("carol") || !reloaded.NavidromeWebOpen() {
		t.Fatalf("policy not persisted: %v", err)
	}
	for _, body := range []string{`{"secureOnlyUsers":["` + strings.Repeat("a", 200) + `"]}`, `{"secureOnlyUsers":["a\nb"]}`} {
		if code, _ := call(body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", body[:30], code)
		}
	}
}

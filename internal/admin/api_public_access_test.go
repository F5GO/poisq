package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/F5GO/poisq/internal/db"
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
	if code, _ := call(http.MethodPost, `{"navidromeWebOpen":false,"pad":"`+strings.Repeat("x", 2048)+`"}`); code != http.StatusBadRequest {
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

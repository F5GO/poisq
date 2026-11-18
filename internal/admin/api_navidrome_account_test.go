package admin

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/security"
)

func TestNavidromeServiceAccount(t *testing.T) {
	var coverCalls atomic.Int32
	nav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sum := md5.Sum([]byte("s3cret" + q.Get("s")))
		ok := q.Get("u") == "studio" && q.Get("t") == hex.EncodeToString(sum[:]) && q.Get("p") == ""
		switch r.URL.Path {
		case "/rest/ping":
			status := "failed"
			if ok {
				status = "ok"
			}
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"` + status + `"}}`))
		case "/rest/getCoverArt":
			coverCalls.Add(1)
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"subsonic-response":{"status":"failed"}}`))
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png"))
		}
	}))
	defer nav.Close()

	tmp := t.TempDir()
	poisqDB, err := db.OpenPoisqDatabase(filepath.Join(tmp, "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer poisqDB.Close()
	cipher, _ := security.NewKeyCipher("test-secret", tmp)
	guard, _ := security.NewPathGuard(filepath.Join(tmp, "music"))
	kp := keypool.NewManager(db.NewKeyRepository(poisqDB), cipher)
	srv := NewAdminServer(nil, nil, nil, nil, kp, nil, nil, nil, nil, guard, nil)
	srv.SetNavidromeURL(nav.URL)

	post := func(body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "http://localhost:4535/api/v1/navidrome/account", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	cover := func() bool {
		rec := httptest.NewRecorder()
		return srv.tryServeFromNavidromeAPIOrCache(rec, httptest.NewRequest(http.MethodGet, "/", nil), "al-1", "1")
	}

	// без учётной записи запасной путь не ходит в Navidrome
	if cover() || coverCalls.Load() != 0 {
		t.Fatalf("без учётной записи обращений к Navidrome быть не должно: %d", coverCalls.Load())
	}

	if code, _ := post(`{"username":"studio","password":"wrong"}`); code != http.StatusBadRequest {
		t.Fatalf("неверный пароль сохранён: HTTP %d", code)
	}
	srv.SetNavidromeURL("http://127.0.0.1:1")
	if code, out := post(`{"username":"studio","password":"s3cret"}`); code != http.StatusBadRequest || strings.Contains(fmt.Sprint(out), "t=") {
		t.Fatalf("недоступный Navidrome: HTTP %d, в ошибке не должно быть адреса с токеном: %v", code, out)
	}
	srv.SetNavidromeURL(nav.URL)
	if code, out := post(`{"username":"studio","password":"s3cret"}`); code != http.StatusOK || out["username"] != "studio" || out["configured"] != true {
		t.Fatalf("сохранение: HTTP %d %v", code, out)
	}
	if keys, _ := kp.ListKeys(); len(keys) != 1 || strings.Contains(keys[0].KeyMask, "s3cret") {
		t.Fatalf("пароль должен храниться одним зашифрованным ключом: %+v", keys)
	}
	if !cover() {
		t.Fatal("обложка не получена от имени служебной учётной записи")
	}

	if code, out := post(`{"clear":true}`); code != http.StatusOK || out["configured"] != false {
		t.Fatalf("удаление: HTTP %d %v", code, out)
	}
	calls := coverCalls.Load()
	if cover() || coverCalls.Load() != calls {
		t.Fatal("после удаления учётной записи запасной путь всё ещё ходит в Navidrome")
	}
}

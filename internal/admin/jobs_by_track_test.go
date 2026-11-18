package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/F5GO/poisq/internal/db"
)

// Баннер на странице «Тексты» спрашивает задачу конкретного трека, а не ищет её в общем списке.
func TestJobsByTrack(t *testing.T) {
	poisqDB, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer poisqDB.Close()
	jobs := db.NewJobRepository(poisqDB)
	for _, id := range []string{"a", "b", "c"} {
		if err := jobs.Enqueue(id, "tournament", 1); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewAdminServer(nil, nil, jobs, nil, nil, nil, nil, nil, nil, nil, nil)
	get := func(trackID string) map[string]*db.LyricsJobRecord {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost:4535/api/v1/jobs?trackId="+trackID, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("trackId=%s: HTTP %d %s", trackID, rec.Code, rec.Body.String())
		}
		var out map[string]*db.LyricsJobRecord
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if j := get("b")["job"]; j == nil || j.TrackID != "b" || j.Status != "pending" {
		t.Fatalf("задача трека b: %+v", j)
	}
	if out := get("missing"); out["job"] != nil {
		t.Fatalf("у трека без задачи ждали job=null: %+v", out)
	}
}

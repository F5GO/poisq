package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/lyrics"
	"github.com/F5GO/poisq/internal/metadata"
	"github.com/F5GO/poisq/internal/security"
	"github.com/F5GO/poisq/internal/tagger"
	"github.com/F5GO/poisq/internal/worker"
)

func newConfidenceTestServer(t *testing.T) (*AdminServer, *db.ConfidenceRepository) {
	t.Helper()
	tmp := t.TempDir()
	music := filepath.Join(tmp, "music")
	_ = os.MkdirAll(filepath.Join(music, "A"), 0o755)
	audio := filepath.Join(music, "A", "song.flac")
	_ = os.WriteFile(audio, []byte("fLaC"), 0o644)
	poisqDB, err := db.OpenPoisqDatabase(filepath.Join(tmp, "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { poisqDB.Close() })
	cipher, _ := security.NewKeyCipher("s", tmp)
	guard, _ := security.NewPathGuard(music)
	trackRepo := db.NewTrackRepository(poisqDB)
	stagingRepo := db.NewStagingRepository(poisqDB)
	jobRepo := db.NewJobRepository(poisqDB)
	cacheRepo := db.NewProviderCacheRepository(poisqDB)
	_ = trackRepo.UpsertTrack(db.TrackRecord{ID: "t1", FilePath: audio, FolderPath: filepath.Dir(audio), Artist: "A", Title: "S", Duration: 100})
	kp := keypool.NewManager(db.NewKeyRepository(poisqDB), cipher)
	lrcWriter := lyrics.NewAtomicLRCWriter(guard)
	coverFetch := metadata.NewCoverFetcher(guard)
	scanner := metadata.NewLibraryScanner(guard.Root(), trackRepo, stagingRepo, nil, nil, coverFetch)
	stagingSvc := tagger.NewStagingService(tagger.NewAtomicTagWriter(guard), coverFetch, trackRepo, stagingRepo)
	workerProc := worker.NewQueueProcessor(jobRepo, trackRepo, cacheRepo, kp, lyrics.NewTournamentOrchestrator(cacheRepo, kp), nil, nil, lrcWriter)
	srv := NewAdminServer(trackRepo, stagingRepo, jobRepo, cacheRepo, kp, scanner, stagingSvc, lrcWriter, workerProc, guard, nil)
	conf := db.NewConfidenceRepository(poisqDB)
	srv.SetConfidenceRepository(conf)
	srv.SetPoisqDB(poisqDB)
	return srv, conf
}

func post(t *testing.T, srv *AdminServer, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Host = "localhost:4535"
	req.Header.Set("Origin", "http://localhost:4535")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestLyricsSaveStoresConfidenceAndCorrections(t *testing.T) {
	srv, conf := newConfidenceTestServer(t)
	rec := post(t, srv, "/api/v1/lyrics/save", `{"trackId":"t1","verified":true,"content":"[00:01.00]one\n[00:05.00]two",
		"confidence":[{"text":"two","sec":5.0,"level":"low","reason":"расходится"}],
		"corrections":[{"text":"one","from":1.4,"to":1.0,"confidence":"high"},{"text":"  ","from":1,"to":2}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: HTTP %d %s", rec.Code, rec.Body.String())
	}
	if n, _ := conf.CountCorrections("t1"); n != 1 {
		t.Fatalf("пустая правка должна отбрасываться, сохранено %d", n)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/lyrics/cache?trackId=t1", nil)
	out := httptest.NewRecorder()
	srv.ServeHTTP(out, req)
	var resp struct {
		Confidence struct {
			Lines []map[string]any `json:"lines"`
		} `json:"confidence"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &resp); err != nil || len(resp.Confidence.Lines) != 1 || resp.Confidence.Lines[0]["level"] != "low" {
		t.Fatalf("уверенность в /lyrics/cache: %s", out.Body.String())
	}

	del := post(t, srv, "/api/v1/lyrics/delete", `{"trackId":"t1"}`)
	if del.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", del.Code, del.Body.String())
	}
	if rec, _ := conf.Get("t1"); rec != nil {
		t.Fatal("уверенность должна удаляться вместе с текстом")
	}
}

func TestLyricsSaveIgnoresMalformedConfidence(t *testing.T) {
	srv, conf := newConfidenceTestServer(t)
	rec := post(t, srv, "/api/v1/lyrics/save", `{"trackId":"t1","content":"[00:01.00]one","confidence":{"not":"an array"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("сохранение не должно падать из-за мусора в уверенности: %d", rec.Code)
	}
	if r, _ := conf.Get("t1"); r != nil {
		t.Fatalf("некорректная уверенность не сохраняется: %+v", r)
	}
}

func TestHealthReportsAlignerState(t *testing.T) {
	srv, _ := newConfidenceTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var resp struct {
		Aligner map[string]any `json:"aligner"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Aligner["configured"] != false {
		t.Fatalf("без ALIGNER_URL сервис не настроен: %s", rec.Body.String())
	}
}

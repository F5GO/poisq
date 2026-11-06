package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/aligner"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/lyrics"
	"github.com/F5GO/poisq/internal/security"
)

type testEnv struct {
	qp        *QueueProcessor
	track     *db.TrackRecord
	confRepo  *db.ConfidenceRepository
	trackRepo *db.TrackRepository
	audio     string
	job       *db.LyricsJobRecord
}

func newTestEnv(t *testing.T, addGroqKey bool) *testEnv {
	t.Helper()
	tmp := t.TempDir()
	music := filepath.Join(tmp, "music")
	if err := os.MkdirAll(filepath.Join(music, "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(music, "A", "song.flac")
	if err := os.WriteFile(audio, []byte("fLaC"), 0o644); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.OpenPoisqDatabase(filepath.Join(tmp, "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	cipher, _ := security.NewKeyCipher("secret", tmp)
	guard, _ := security.NewPathGuard(music)
	kp := keypool.NewManager(db.NewKeyRepository(sqlDB), cipher)
	if addGroqKey {
		if _, err := kp.AddKey("groq", "test", "gsk_test_key"); err != nil {
			t.Fatal(err)
		}
	}
	trackRepo := db.NewTrackRepository(sqlDB)
	if err := trackRepo.UpsertTrack(db.TrackRecord{ID: "t1", FilePath: audio, FolderPath: filepath.Dir(audio),
		Artist: "Artist", Title: "Song", Duration: 200, LangCode: "en"}); err != nil {
		t.Fatal(err)
	}
	track, err := trackRepo.GetByID("t1")
	if err != nil {
		t.Fatal(err)
	}
	conf := db.NewConfidenceRepository(sqlDB)
	qp := NewQueueProcessor(db.NewJobRepository(sqlDB), trackRepo, db.NewProviderCacheRepository(sqlDB), kp,
		nil, nil, nil, lyrics.NewAtomicLRCWriter(guard))
	qp.alignerPoll = 10 * time.Millisecond
	return &testEnv{qp: qp, track: track, confRepo: conf, trackRepo: trackRepo, audio: audio, job: &db.LyricsJobRecord{ID: 1, TrackID: "t1", JobType: "whisper_karaoke"}}
}

const threeLines = "First line here\nSecond line here\nThird line here"

func fakeAlignerServer(t *testing.T, onSubmit func(map[string]any), final string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if onSubmit != nil {
				onSubmit(body)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"j1","status":"queued"}`))
		default:
			_, _ = w.Write([]byte(final))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const doneResult = `{"id":"j1","status":"done","progress":[{"t":1,"msg":"[asr-anchor] этап"}],"result":{
 "lrc":"[00:09.50]First line here\n[00:14.00]Second line here\n[00:19.20]Third line here",
 "lines":[
  {"index":0,"text":"First line here","start":10,"end":13,"lrc_time":9.5,"confidence":"high"},
  {"index":1,"text":"Second line here","start":14.5,"end":17,"lrc_time":14.0,"confidence":"low","reason":"расходится"},
  {"index":2,"text":"Third line here","start":19.7,"end":22,"lrc_time":19.2,"confidence":"medium"}],
 "summary":{"lines":3,"high":1,"medium":1,"low":1,"low_ratio":0.333,"seconds":12}}}`

func TestRunAcousticAlignSavesLRCAndConfidence(t *testing.T) {
	env := newTestEnv(t, true)
	var sent map[string]any
	srv := fakeAlignerServer(t, func(b map[string]any) { sent = b }, strings.Replace(doneResult, `"low_ratio":0.333`, `"low_ratio":0.1`, 1))
	env.qp.SetAligner(aligner.NewClient(srv.URL, ""), env.confRepo, "", "")

	if err := env.qp.runAcousticAlign(context.Background(), env.job, env.track, "[00:01.00]First line here\n[00:05.00]Second line here\n[00:09.00]Third line here"); err != nil {
		t.Fatalf("runAcousticAlign: %v", err)
	}
	// в сервис ушёл текст без таймкодов, ключ Groq из пула и путь к файлу
	if lines, _ := sent["lines"].([]any); len(lines) != 3 || lines[0] != "First line here" {
		t.Fatalf("в сервис должен уходить чистый текст: %v", sent["lines"])
	}
	if sent["groq_key"] != "gsk_test_key" || sent["audio_path"] != env.audio || sent["lang"] != "en" {
		t.Fatalf("неверная заявка: %v", sent)
	}
	// .lrc записан рядом с аудио
	data, err := os.ReadFile(strings.TrimSuffix(env.audio, ".flac") + ".lrc")
	if err != nil || !strings.Contains(string(data), "[00:14.00]Second line here") {
		t.Fatalf(".lrc не записан: %v %q", err, data)
	}
	// статус трека в базе и уверенность по строкам
	tr, _ := env.trackRepo.GetByID("t1")
	if tr.ActiveSource != "poisq" || tr.LyricsStatus != "verified" {
		t.Fatalf("статус трека: %+v", tr)
	}
	rec, err := env.confRepo.Get("t1")
	if err != nil || rec == nil {
		t.Fatalf("уверенность не сохранена: %v", err)
	}
	var lines []map[string]any
	if err := json.Unmarshal([]byte(rec.LinesJSON), &lines); err != nil || len(lines) != 3 || lines[1]["level"] != "low" || lines[1]["sec"].(float64) != 14.0 {
		t.Fatalf("неверная уверенность: %s", rec.LinesJSON)
	}
}

func TestLowConfidenceRatioMakesLyricsUnverified(t *testing.T) {
	env := newTestEnv(t, true)
	bad := strings.Replace(doneResult, `"low_ratio":0.333`, `"low_ratio":0.6`, 1)
	srv := fakeAlignerServer(t, nil, bad)
	env.qp.SetAligner(aligner.NewClient(srv.URL, ""), env.confRepo, "", "")
	if err := env.qp.runAcousticAlign(context.Background(), env.job, env.track, threeLines); err != nil {
		t.Fatal(err)
	}
	tr, _ := env.trackRepo.GetByID("t1")
	if tr.LyricsStatus != "synced" {
		t.Fatalf("при низкой достоверности текст не должен быть «verified», статус: %s", tr.LyricsStatus)
	}
}

func TestQuotaErrorBecomesWaitNotFailure(t *testing.T) {
	env := newTestEnv(t, true)
	srv := fakeAlignerServer(t, nil, `{"id":"j1","status":"error","error":"Groq HTTP 429","error_code":"quota"}`)
	env.qp.SetAligner(aligner.NewClient(srv.URL, ""), env.confRepo, "", "")
	err := env.qp.runAcousticAlign(context.Background(), env.job, env.track, threeLines)
	var qw *QuotaWaitError
	if !errors.As(err, &qw) || qw.Wait < 5*time.Minute {
		t.Fatalf("ожидалось QuotaWaitError, получено %v", err)
	}
}

func TestUnavailableServiceIsReportedForFallback(t *testing.T) {
	env := newTestEnv(t, true)
	env.qp.SetAligner(aligner.NewClient("http://127.0.0.1:1", ""), env.confRepo, "", "")
	err := env.qp.runAcousticAlign(context.Background(), env.job, env.track, threeLines)
	if !errors.Is(err, aligner.ErrUnavailable) {
		t.Fatalf("недоступность сервиса должна быть ErrUnavailable (для отката на облачный режим): %v", err)
	}
}

func TestNoGroqKeyStopsBeforeCallingService(t *testing.T) {
	env := newTestEnv(t, false)
	called := false
	srv := fakeAlignerServer(t, func(map[string]any) { called = true }, doneResult)
	env.qp.SetAligner(aligner.NewClient(srv.URL, ""), env.confRepo, "", "")
	err := env.qp.runAcousticAlign(context.Background(), env.job, env.track, threeLines)
	if !errors.Is(err, db.ErrNoAvailableKeys) || called {
		t.Fatalf("без ключа Groq сервис вызываться не должен: err=%v called=%v", err, called)
	}
}

func TestPathMappingAndTooFewLines(t *testing.T) {
	env := newTestEnv(t, true)
	env.qp.SetAligner(nil, nil, "/music", "/mnt/media")
	if got := env.qp.mapAlignerPath("/music/A/x.flac"); got != "/mnt/media/A/x.flac" {
		t.Fatalf("маппинг пути: %s", got)
	}
	if got := env.qp.mapAlignerPath("/other/x.flac"); got != "/other/x.flac" {
		t.Fatalf("чужой путь не меняется: %s", got)
	}
	env.qp.SetAligner(aligner.NewClient("http://127.0.0.1:1", ""), nil, "", "")
	if err := env.qp.runAcousticAlign(context.Background(), env.job, env.track, "one\ntwo"); err == nil {
		t.Fatal("для 2 строк выравнивание не запускается")
	}
}

func TestSplitLyricsLinesStripsTimestampsAndBlanks(t *testing.T) {
	got := splitLyricsLines("[00:01.00] A line\n\n[00:05.50]B line  \nplain line")
	if len(got) != 3 || got[0] != "A line" || got[1] != "B line" || got[2] != "plain line" {
		t.Fatalf("splitLyricsLines: %q", got)
	}
}

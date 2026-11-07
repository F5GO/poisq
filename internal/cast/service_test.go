package cast

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/F5GO/poisq/internal/db"
)

func TestRelativePath(t *testing.T) {
	cases := []struct {
		in    string
		roots []string
		want  string
		ok    bool
	}{
		{"Artist/Album/01.flac", nil, "Artist/Album/01.flac", true},            // Navidrome 0.55+
		{"/music/Artist/01.flac", []string{"/music/"}, "Artist/01.flac", true}, // старый абсолютный путь
		{"/other/01.flac", []string{"/music"}, "", false},
		{"/musicx/01.flac", []string{"/music"}, "", false}, // префикс не по границе папки
		{"", nil, "", false},
	}
	for _, c := range cases {
		got, ok := relativePath(c.in, c.roots)
		if got != c.want || ok != c.ok {
			t.Errorf("relativePath(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

type maCall struct {
	Command string         `json:"command"`
	Args    map[string]any `json:"args"`
}

// fakeMA записывает команды и отвечает как Music Assistant.
func fakeMA(t *testing.T) (*httptest.Server, func() []maCall) {
	var mu sync.Mutex
	var calls []maCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var c maCall
		_ = json.NewDecoder(r.Body).Decode(&c)
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		switch c.Command {
		case "players/all":
			_, _ = w.Write([]byte(`[{"player_id":"kitchen","name":"Кухня","available":true,"enabled":true,"playback_state":"idle","volume_level":30},
				{"player_id":"hidden","name":"Спальня","available":true,"enabled":true}]`))
		case "player_queues/get":
			_, _ = w.Write([]byte(`{"current_index":2,"state":"paused","elapsed_time":41.5,"elapsed_time_last_updated":0}`))
		case "players/get":
			_, _ = w.Write([]byte(`{"player_id":"kitchen","volume_level":30}`))
		default:
			_, _ = w.Write([]byte(`null`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []maCall { mu.Lock(); defer mu.Unlock(); return append([]maCall(nil), calls...) }
}

func newTestService(t *testing.T, maURL string) *Service {
	dir := t.TempDir()
	poisqDB, err := db.OpenPoisqDatabase(filepath.Join(dir, "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { poisqDB.Close() })

	navPath := filepath.Join(dir, "navidrome.db")
	nav, err := sql.Open("sqlite", navPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE media_file (id TEXT PRIMARY KEY, path TEXT)`,
		`CREATE TABLE library (id INTEGER PRIMARY KEY, path TEXT)`,
		`INSERT INTO library (path) VALUES ('/music')`,
		`INSERT INTO media_file VALUES ('s1', 'Кино/Группа крови/01 Группа крови.flac'), ('s2', '/music/Artist/02.m4a')`,
	} {
		if _, err := nav.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	nav.Close()

	s := &Service{
		DB:        poisqDB,
		Token:     func() (string, error) { return "tok", nil },
		OpenNavDB: func() (*sql.DB, error) { return db.OpenDatabase(navPath) },
		HTTP:      http.DefaultClient,
	}
	if err := s.SaveSettings(Settings{URL: maURL + "/", Provider: "filesystem_local--abc", Players: []string{"kitchen"}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func call(t *testing.T, s *Service, query string) map[string]any {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rest/"+query, nil))
	var root map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatalf("%s: %v: %s", query, err, rec.Body.String())
	}
	return root["subsonic-response"]
}

func TestHandlerFlow(t *testing.T) {
	ma, calls := fakeMA(t)
	s := newTestService(t, ma.URL)

	// Видна только колонка, разрешённая в Studio
	r := call(t, s, "poisqCastZones")
	zones, _ := r["castZones"].([]any)
	if r["status"] != "ok" || len(zones) != 1 || zones[0].(map[string]any)["id"] != "kitchen" {
		t.Fatalf("zones: %v", r)
	}

	// Чужая колонка запрещена
	if r := call(t, s, "poisqCastPlay?zone=hidden&id=s1"); r["status"] != "failed" {
		t.Fatalf("hidden zone must fail: %v", r)
	}

	// Очередь из двух песен: URI файлового провайдера MA, старт со второй песни с 15-й секунды
	if r := call(t, s, "poisqCastPlay?zone=kitchen&id=s1&id=s2&index=1&offset=15"); r["status"] != "ok" {
		t.Fatalf("play: %v", r)
	}
	if r := call(t, s, "poisqCastControl?zone=kitchen&action=volume&volume=150"); r["status"] != "ok" {
		t.Fatalf("volume: %v", r)
	}
	if r := call(t, s, "poisqCastControl?zone=kitchen&action=remove&position=3"); r["status"] != "ok" {
		t.Fatalf("remove: %v", r)
	}
	if r := call(t, s, "poisqCastControl?zone=kitchen&action=index&position=4"); r["status"] != "ok" {
		t.Fatalf("index: %v", r)
	}
	if r := call(t, s, "poisqCastControl?zone=kitchen&action=explode"); r["status"] != "failed" {
		t.Fatalf("unknown action must fail: %v", r)
	}

	st := call(t, s, "poisqCastStatus?zone=kitchen")["castStatus"].(map[string]any)
	if st["state"] != "paused" || st["index"] != float64(2) || st["position"] != 41.5 || st["volume"] != float64(30) {
		t.Fatalf("status: %v", st)
	}

	var play, index, jump, volume, remove *maCall
	for _, c := range calls() {
		c := c
		switch c.Command {
		case "player_queues/play_media":
			play = &c
		case "player_queues/play_index":
			if index == nil {
				index = &c
			} else {
				jump = &c
			}
		case "players/cmd/volume_set":
			volume = &c
		case "player_queues/delete_item":
			remove = &c
		}
	}
	if play == nil || play.Args["queue_id"] != "kitchen" || play.Args["option"] != "replace" {
		t.Fatalf("play_media: %+v", play)
	}
	media := play.Args["media"].([]any)
	want := []string{"filesystem_local--abc://track/Кино/Группа крови/01 Группа крови.flac", "filesystem_local--abc://track/Artist/02.m4a"}
	if len(media) != 2 || media[0] != want[0] || media[1] != want[1] {
		t.Fatalf("media: %v", media)
	}
	if index == nil || index.Args["index"] != float64(1) || index.Args["seek_position"] != float64(15) {
		t.Fatalf("play_index: %+v", index)
	}
	if jump == nil || jump.Args["index"] != float64(4) || jump.Args["seek_position"] != nil {
		t.Fatalf("index action: %+v", jump)
	}
	if remove == nil || remove.Args["item_id_or_index"] != float64(3) {
		t.Fatalf("delete_item: %+v", remove)
	}
	if volume == nil || volume.Args["volume_level"] != float64(100) {
		t.Fatalf("volume_set must clamp to 100: %+v", volume)
	}
}

func TestNotConfigured(t *testing.T) {
	ma, _ := fakeMA(t)
	s := newTestService(t, ma.URL)
	if err := s.SaveSettings(Settings{URL: "ftp://x"}); err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("bad url must be rejected: %v", err)
	}
	if err := s.SaveSettings(Settings{URL: ma.URL}); err != nil { // без источника и колонок
		t.Fatal(err)
	}
	if r := call(t, s, "poisqCastZones"); r["status"] != "failed" {
		t.Fatalf("zones without provider must fail: %v", r)
	}
}

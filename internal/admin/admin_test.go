// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл admin_test.go содержит сквозные интеграционные тесты REST API админки,
// проверки CSRF-защиты, ротации ключей, атомарного сохранения .lrc файлов рядом с треками
// и раздачи встроенного через //go:embed фронтенда.
package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
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

func TestAdminServerEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	musicDir := filepath.Join(tmpDir, "music")
	_ = os.MkdirAll(filepath.Join(musicDir, "Kino"), 0755)

	audioPath := filepath.Join(musicDir, "Kino", "Gruppa_Krovi.flac")
	_ = os.WriteFile(audioPath, []byte("fLaC-dummy"), 0644)

	poisqDB, err := db.OpenPoisqDatabase(filepath.Join(tmpDir, "poisq.db"))
	if err != nil {
		t.Fatalf("OpenPoisqDatabase: %v", err)
	}
	defer poisqDB.Close()

	cipher, _ := security.NewKeyCipher("test-admin-secret", tmpDir)
	guard, _ := security.NewPathGuard(musicDir)

	keyRepo := db.NewKeyRepository(poisqDB)
	trackRepo := db.NewTrackRepository(poisqDB)
	stagingRepo := db.NewStagingRepository(poisqDB)
	jobRepo := db.NewJobRepository(poisqDB)
	cacheRepo := db.NewProviderCacheRepository(poisqDB)

	_ = trackRepo.UpsertTrack(db.TrackRecord{
		ID:         "track-1",
		FilePath:   audioPath,
		FolderPath: filepath.Dir(audioPath),
		Artist:     "Кино",
		Album:      "Группа крови",
		Title:      "Группа крови",
		Duration:   245,
	})

	kp := keypool.NewManager(keyRepo, cipher)
	lrcWriter := lyrics.NewAtomicLRCWriter(guard)
	tournament := lyrics.NewTournamentOrchestrator(cacheRepo, kp)
	coverFetch := metadata.NewCoverFetcher(guard)
	scanner := metadata.NewLibraryScanner(guard.Root(), trackRepo, stagingRepo, nil, coverFetch)
	stagingSvc := tagger.NewStagingService(tagger.NewAtomicTagWriter(guard), coverFetch, trackRepo, stagingRepo)
	workerProc := worker.NewQueueProcessor(jobRepo, trackRepo, cacheRepo, kp, tournament, nil, nil, lrcWriter)

	srv := NewAdminServer(trackRepo, stagingRepo, jobRepo, cacheRepo, kp, scanner, stagingSvc, lrcWriter, workerProc, guard, nil)

	// 1. Проверка раздачи встроенного через //go:embed фронтенда (/)
	reqUI := httptest.NewRequest(http.MethodGet, "http://localhost:4535/", nil)
	recUI := httptest.NewRecorder()
	srv.ServeHTTP(recUI, reqUI)
	if recUI.Code != http.StatusOK || !strings.Contains(recUI.Body.String(), "poisq Studio") {
		t.Fatalf("expected embedded UI on /, got HTTP %d", recUI.Code)
	}

	// 2. Проверка CSRF-защиты от чужого Origin
	reqCSRF := httptest.NewRequest(http.MethodPost, "http://localhost:4535/api/v1/keys", strings.NewReader(`{}`))
	reqCSRF.Host = "localhost:4535"
	reqCSRF.Header.Set("Origin", "https://evil-attacker.example.com")
	recCSRF := httptest.NewRecorder()
	srv.ServeHTTP(recCSRF, reqCSRF)
	if recCSRF.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for cross-origin POST, got %d", recCSRF.Code)
	}

	// 3. Добавление ключа в пул и проверка маскирования
	keyBody, _ := json.Marshal(map[string]string{
		"provider": "gemini",
		"label":    "Main Gemini",
		"apiKey":   "AIzaSySecretKey987654321",
	})
	reqAddKey := httptest.NewRequest(http.MethodPost, "http://localhost:4535/api/v1/keys", bytes.NewReader(keyBody))
	reqAddKey.Host = "localhost:4535"
	reqAddKey.Header.Set("Origin", "http://localhost:4535")
	recAddKey := httptest.NewRecorder()
	srv.ServeHTTP(recAddKey, reqAddKey)
	if recAddKey.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for key, got %d: %s", recAddKey.Code, recAddKey.Body.String())
	}

	// 4. Сохранение текста в Менеджере LRC и проверка атомарного создания файла Gruppa_Krovi.lrc рядом с .flac
	lrcPayload, _ := json.Marshal(map[string]any{
		"trackId":  "track-1",
		"source":   "yandex",
		"content":  "[00:14.20] Теплое место, но улицы ждут",
		"verified": true,
	})
	reqSaveLRC := httptest.NewRequest(http.MethodPost, "http://localhost:4535/api/v1/lyrics/save", bytes.NewReader(lrcPayload))
	recSaveLRC := httptest.NewRecorder()
	srv.ServeHTTP(recSaveLRC, reqSaveLRC)
	if recSaveLRC.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on lyrics save, got %d: %s", recSaveLRC.Code, recSaveLRC.Body.String())
	}

	expectedLrcFile := filepath.Join(musicDir, "Kino", "Gruppa_Krovi.lrc")
	lrcOnDisk, err := os.ReadFile(expectedLrcFile)
	if err != nil {
		t.Fatalf("expected .lrc file to be atomically written at %s: %v", expectedLrcFile, err)
	}
	if !strings.Contains(string(lrcOnDisk), "[00:14.20] Теплое место") {
		t.Fatalf("unexpected .lrc content on disk: %q", string(lrcOnDisk))
	}

	// 5. Проверка безопасной отдачи локальной обложки по пути к папке альбома
	coverFile := filepath.Join(musicDir, "Kino", "cover.jpg")
	_ = os.WriteFile(coverFile, []byte("\xFF\xD8\xFF\xE0\x00\x10JFIFdummy"), 0644)

	reqCover := httptest.NewRequest(http.MethodGet, "http://localhost:4535/api/v1/covers/local?path="+filepath.Join(musicDir, "Kino"), nil)
	recCover := httptest.NewRecorder()
	srv.ServeHTTP(recCover, reqCover)
	if recCover.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for local cover by folder path, got %d: %s", recCover.Code, recCover.Body.String())
	}
	if !bytes.Contains(recCover.Body.Bytes(), []byte("JFIFdummy")) {
		t.Fatalf("expected cover file content to be served")
	}

	// 6. Проверка эндпоинта кандидатов обложек
	reqCand := httptest.NewRequest(http.MethodGet, "http://localhost:4535/api/v1/covers/candidates?artist=Kino&album=Gruppa", nil)
	recCand := httptest.NewRecorder()
	srv.ServeHTTP(recCand, reqCand)
	if recCand.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /api/v1/covers/candidates, got %d", recCand.Code)
	}

	// 7. Проверка получения обложки альбома и фото исполнителя (включая кириллицу и подпапки)
	reqAlbCover := httptest.NewRequest(http.MethodGet, "http://localhost:4535/api/v1/navidrome/album-cover?album=%D0%93%D1%80%D1%83%D0%BF%D0%BF%D0%B0+%D0%BA%D1%80%D0%BE%D0%B2%D0%B8&artist=%D0%9A%D0%B8%D0%BD%D0%BE", nil)
	recAlbCover := httptest.NewRecorder()
	srv.ServeHTTP(recAlbCover, reqAlbCover)
	if recAlbCover.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /api/v1/navidrome/album-cover, got %d: %s", recAlbCover.Code, recAlbCover.Body.String())
	}

	reqArtImg := httptest.NewRequest(http.MethodGet, "http://localhost:4535/api/v1/navidrome/artist-image?artist=%D0%9A%D0%B8%D0%BD%D0%BE", nil)
	recArtImg := httptest.NewRecorder()
	srv.ServeHTTP(recArtImg, reqArtImg)
	if recArtImg.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /api/v1/navidrome/artist-image, got %d: %s", recArtImg.Code, recArtImg.Body.String())
	}

	// 8. Проверка полного сброса БД (/api/v1/db/reset)
	srv.SetPoisqDB(poisqDB)
	reqReset := httptest.NewRequest(http.MethodPost, "http://localhost:4535/api/v1/db/reset", strings.NewReader(`{"keepKeys":true}`))
	recReset := httptest.NewRecorder()
	srv.ServeHTTP(recReset, reqReset)
	if recReset.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /api/v1/db/reset, got %d: %s", recReset.Code, recReset.Body.String())
	}
	tracksAfter, _ := trackRepo.ListTracks("all", "", 10)
	if len(tracksAfter) != 0 {
		t.Fatalf("expected 0 tracks after DB reset, got %d", len(tracksAfter))
	}

	// 9. Проверка раздачи обновленных брендовых ассетов и модульных стилей
	staticEndpoints := []struct {
		url          string
		expectedCode int
		containsBody string
	}{
		{"/", http.StatusOK, "poisq — Умный поиск музыки и текстов"},
		{"/styles.css", http.StatusOK, "@import './css/tokens.css'"},
		{"/css/tokens.css", http.StatusOK, "--signal: #2438E0;"},
		{"/assets/brand/poisq-mark-192.png", http.StatusOK, ""},
		{"/favicon.png", http.StatusOK, ""},
	}
	// Отладочный список файлов не выходит за пределы медиатеки
	for _, sub := range []string{"../../etc", "/etc"} {
		reqFS := httptest.NewRequest(http.MethodGet, "http://localhost:4535/api/v1/debug/fs?sub="+url.QueryEscape(sub), nil)
		recFS := httptest.NewRecorder()
		srv.ServeHTTP(recFS, reqFS)
		if recFS.Code != http.StatusBadRequest {
			t.Fatalf("debug/fs?sub=%s: ожидался 400, получено %d", sub, recFS.Code)
		}
	}
	for _, ep := range staticEndpoints {
		reqStatic := httptest.NewRequest(http.MethodGet, ep.url, nil)
		reqStatic.Host = "localhost:4535"
		recStatic := httptest.NewRecorder()
		srv.ServeHTTP(recStatic, reqStatic)
		if recStatic.Code != ep.expectedCode {
			t.Errorf("expected %d on %s, got %d", ep.expectedCode, ep.url, recStatic.Code)
		}
		if ep.containsBody != "" && !strings.Contains(recStatic.Body.String(), ep.containsBody) {
			t.Errorf("expected body of %s to contain %q", ep.url, ep.containsBody)
		}
	}
}

func TestTrackTagsManualEditAndUndo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg не найден")
	}
	tmpDir := t.TempDir()
	musicDir := filepath.Join(tmpDir, "music")
	_ = os.MkdirAll(filepath.Join(musicDir, "Kino"), 0755)
	audioPath := filepath.Join(musicDir, "Kino", "song.flac")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono", "-t", "0.2", "-metadata", "title=Old", audioPath).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}

	poisqDB, err := db.OpenPoisqDatabase(filepath.Join(tmpDir, "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer poisqDB.Close()
	guard, _ := security.NewPathGuard(musicDir)
	trackRepo := db.NewTrackRepository(poisqDB)
	stagingRepo := db.NewStagingRepository(poisqDB)
	_ = trackRepo.UpsertTrack(db.TrackRecord{ID: "t1", FilePath: audioPath, FolderPath: filepath.Dir(audioPath), Artist: "Кино", Title: "Old"})
	coverFetch := metadata.NewCoverFetcher(guard)
	stagingSvc := tagger.NewStagingService(tagger.NewAtomicTagWriter(guard), coverFetch, trackRepo, stagingRepo)
	srv := NewAdminServer(trackRepo, stagingRepo, db.NewJobRepository(poisqDB), db.NewProviderCacheRepository(poisqDB), nil, metadata.NewLibraryScanner(guard.Root(), trackRepo, stagingRepo, nil, coverFetch), stagingSvc, nil, nil, guard, nil)

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://localhost:4535"+path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	rec := post("/api/v1/tracks/tags", `{"trackId":"t1","artist":"Кино","title":"Новое имя","album":"Альбом","year":1988}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tags: %d %s", rec.Code, rec.Body.String())
	}
	var res struct {
		StagingID int64 `json:"stagingId"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if tr, _ := trackRepo.GetByID("t1"); tr.Title != "Новое имя" || tr.Year != 1988 {
		t.Fatalf("БД не обновлена: %+v", tr)
	}
	probeTag := func(tag string) string {
		out, _ := exec.Command("ffprobe", "-loglevel", "error", "-show_entries", "format_tags="+tag, "-of", "default=nw=1:nk=1", audioPath).Output()
		return strings.TrimSpace(string(out))
	}
	probe := func() string { return probeTag("title") }
	if got := probe(); got != "Новое имя" {
		t.Fatalf("тег в файле: %q", got)
	}
	if got := probeTag("album_artist"); got != "" {
		t.Fatalf("ручная правка добавила album_artist: %q", got)
	}

	if rec := post("/api/v1/staging/undo", fmt.Sprintf(`{"id":%d}`, res.StagingID)); rec.Code != http.StatusOK {
		t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
	}
	if got := probe(); got != "Old" {
		t.Fatalf("после отката тег в файле: %q", got)
	}

	if rec := post("/api/v1/tracks/tags", `{"trackId":"nope","title":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("неизвестный трек: ожидался 400, получено %d", rec.Code)
	}
}

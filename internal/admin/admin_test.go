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
	scanner := metadata.NewLibraryScanner(guard.Root(), trackRepo, stagingRepo, nil, nil, coverFetch)
	stagingSvc := tagger.NewStagingService(tagger.NewAtomicTagWriter(guard), coverFetch, trackRepo, stagingRepo)
	workerProc := worker.NewQueueProcessor(jobRepo, trackRepo, cacheRepo, kp, tournament, nil, nil, lrcWriter)

	srv := NewAdminServer(trackRepo, stagingRepo, jobRepo, cacheRepo, kp, scanner, stagingSvc, lrcWriter, workerProc, guard, nil)

	// 1. Проверка раздачи встроенного через //go:embed фронтенда (/)
	reqUI := httptest.NewRequest(http.MethodGet, "/", nil)
	recUI := httptest.NewRecorder()
	srv.ServeHTTP(recUI, reqUI)
	if recUI.Code != http.StatusOK || !strings.Contains(recUI.Body.String(), "poisq Studio") {
		t.Fatalf("expected embedded UI on /, got HTTP %d", recUI.Code)
	}

	// 2. Проверка CSRF-защиты от чужого Origin
	reqCSRF := httptest.NewRequest(http.MethodPost, "/api/v1/keys", strings.NewReader(`{}`))
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
	reqAddKey := httptest.NewRequest(http.MethodPost, "/api/v1/keys", bytes.NewReader(keyBody))
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
	reqSaveLRC := httptest.NewRequest(http.MethodPost, "/api/v1/lyrics/save", bytes.NewReader(lrcPayload))
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

	reqCover := httptest.NewRequest(http.MethodGet, "/api/v1/covers/local?path="+filepath.Join(musicDir, "Kino"), nil)
	recCover := httptest.NewRecorder()
	srv.ServeHTTP(recCover, reqCover)
	if recCover.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for local cover by folder path, got %d: %s", recCover.Code, recCover.Body.String())
	}
	if !bytes.Contains(recCover.Body.Bytes(), []byte("JFIFdummy")) {
		t.Fatalf("expected cover file content to be served")
	}

	// 6. Проверка эндпоинта кандидатов обложек
	reqCand := httptest.NewRequest(http.MethodGet, "/api/v1/covers/candidates?artist=Kino&album=Gruppa", nil)
	recCand := httptest.NewRecorder()
	srv.ServeHTTP(recCand, reqCand)
	if recCand.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /api/v1/covers/candidates, got %d", recCand.Code)
	}
}

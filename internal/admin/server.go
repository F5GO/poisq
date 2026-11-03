// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл server.go инициализирует маршрутизатор админки, встраивает статические
// ассеты фронтенда через директиву //go:embed и оборачивает эндпоинты в защитный контур AdminGuardMiddleware.
package admin

import (
	"database/sql"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/lyrics"
	"github.com/F5GO/poisq/internal/metadata"
	"github.com/F5GO/poisq/internal/security"
	"github.com/F5GO/poisq/internal/tagger"
	"github.com/F5GO/poisq/internal/worker"
)

//go:embed ui/*
var embeddedUI embed.FS

// AdminServer объединяет все сервисы и репозитории для работы веб-интерфейса на порту :4535.
type AdminServer struct {
	tracksRepo  *db.TrackRepository
	stagingRepo *db.StagingRepository
	jobRepo     *db.JobRepository
	cacheRepo   *db.ProviderCacheRepository
	keyPool     *keypool.Manager
	scanner     *metadata.LibraryScanner
	stagingSvc  *tagger.StagingService
	lrcWriter   *lyrics.AtomicLRCWriter
	worker      *worker.QueueProcessor
	guard       *security.PathGuard
	openNavDB   func() (*sql.DB, error)
	arbiter     *ai.MetadataArbiter
	handler     http.Handler
}

// NewAdminServer конструирует HTTP-сервер панели управления poisq.
func NewAdminServer(
	tracksRepo *db.TrackRepository,
	stagingRepo *db.StagingRepository,
	jobRepo *db.JobRepository,
	cacheRepo *db.ProviderCacheRepository,
	keyPool *keypool.Manager,
	scanner *metadata.LibraryScanner,
	stagingSvc *tagger.StagingService,
	lrcWriter *lyrics.AtomicLRCWriter,
	workerProc *worker.QueueProcessor,
	guard *security.PathGuard,
	openNavDB func() (*sql.DB, error),
) *AdminServer {
	s := &AdminServer{
		tracksRepo:  tracksRepo,
		stagingRepo: stagingRepo,
		jobRepo:     jobRepo,
		cacheRepo:   cacheRepo,
		keyPool:     keyPool,
		scanner:     scanner,
		stagingSvc:  stagingSvc,
		lrcWriter:   lrcWriter,
		worker:      workerProc,
		guard:       guard,
		openNavDB:   openNavDB,
	}

	mux := http.NewServeMux()

	// REST API v1
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/scan/sync", s.handleScanSync)
	mux.HandleFunc("/api/v1/scan/audit", s.handleScanAudit)

	mux.HandleFunc("/api/v1/staging", s.handleStagingList)
	mux.HandleFunc("/api/v1/staging/apply", s.handleStagingApply)
	mux.HandleFunc("/api/v1/staging/ignore", s.handleStagingIgnore)
	mux.HandleFunc("/api/v1/staging/undo", s.handleStagingUndo)

	mux.HandleFunc("/api/v1/tracks", s.handleTracksList)
	mux.HandleFunc("/api/v1/tracks/stream", s.handleTrackStream)
	mux.HandleFunc("/api/v1/covers/local", s.handleLocalCover)
	mux.HandleFunc("/api/v1/covers/candidates", s.handleCoverCandidates)
	mux.HandleFunc("/api/v1/covers/ai-resolve", s.handleCoverAIResolve)
	mux.HandleFunc("/api/v1/lyrics/cache", s.handleLyricsCache)
	mux.HandleFunc("/api/v1/lyrics/save", s.handleLyricsSave)
	mux.HandleFunc("/api/v1/lyrics/enqueue", s.handleLyricsEnqueue)
	mux.HandleFunc("/api/v1/jobs", s.handleJobsList)

	mux.HandleFunc("/api/v1/keys", s.handleKeysAPI)

	// Встроенный фронтенд (SPA) через //go:embed
	uiSub, err := fs.Sub(embeddedUI, "ui")
	if err == nil {
		fileServer := http.FileServer(http.FS(uiSub))
		mux.Handle("/", fileServer)
	}

	s.handler = security.AdminGuardMiddleware(mux)
	return s
}

// ServeHTTP реализует интерфейс http.Handler.
func (s *AdminServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

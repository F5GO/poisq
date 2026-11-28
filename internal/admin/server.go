// Package admin реализует внутренний HTTP-сервер панели управления (:4535),
// предоставляющий REST API и современный веб-интерфейс для управления медиатекой.
//
// Данный файл server.go инициализирует маршрутизатор админки, встраивает статические
// ассеты фронтенда через директиву //go:embed и оборачивает эндпоинты в защитный контур AdminGuardMiddleware.
package admin

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/aligner"
	"github.com/F5GO/poisq/internal/artistbio"
	"github.com/F5GO/poisq/internal/cast"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/lyrics"
	"github.com/F5GO/poisq/internal/metadata"
	"github.com/F5GO/poisq/internal/securetransport"
	"github.com/F5GO/poisq/internal/security"
	"github.com/F5GO/poisq/internal/tagger"
	"github.com/F5GO/poisq/internal/worker"
)

//go:embed ui/*
var embeddedUI embed.FS

// AdminServer объединяет все сервисы и репозитории для работы веб-интерфейса на порту :4535.
type AdminServer struct {
	tracksRepo      *db.TrackRepository
	stagingRepo     *db.StagingRepository
	jobRepo         *db.JobRepository
	cacheRepo       *db.ProviderCacheRepository
	keyPool         *keypool.Manager
	scanner         *metadata.LibraryScanner
	stagingSvc      *tagger.StagingService
	lrcWriter       *lyrics.AtomicLRCWriter
	worker          *worker.QueueProcessor
	guard           *security.PathGuard
	openNavDB       func() (*sql.DB, error)
	poisqDB         *sql.DB
	arbiter         *ai.MetadataArbiter
	navidromeURL    string
	confidence      *db.ConfidenceRepository
	alignerCli      *aligner.Client
	cast            *cast.Service
	publicAccess    *security.PublicAccess
	secureTransport *securetransport.Installation
	secureAccounts  *securetransport.Accounts
	biographies     *artistbio.Store
	biographyJobs   interface {
		Request(context.Context, string, bool) (<-chan struct{}, artistbio.JobState, error)
	}
	metadataRepo   *db.MetadataRepository
	mediaInfoRepo  *db.MediaInfoRepository
	policyRepo     *db.MetadataPolicyRepository
	analysisRepo   *db.AudioAnalysisRepository
	writebackRepo  *db.WritebackRepository
	historyRepo    *db.MetadataHistoryRepository
	analysisSvc    *metadata.AudioAnalysisService
	analysisJobMu  sync.Mutex
	analysisJobs   map[string]bool
	analysisSlots  chan struct{}
	nfoSvc         *metadata.NFOService
	enrichmentRepo *db.MediaEnrichmentRepository
	enrichmentSvc  *metadata.MediaEnrichmentService
	auditMu        sync.RWMutex
	auditStatus    metadataAuditStatus
	handler        http.Handler
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
	mux.HandleFunc("/api/v1/metadata/track", s.handleMetadataTrack)
	mux.HandleFunc("/api/v1/metadata/tags/preview", s.handleMetadataTagsPreview)
	mux.HandleFunc("/api/v1/metadata/tags/apply", s.handleMetadataTagsApply)
	mux.HandleFunc("/api/v1/metadata/history", s.handleMetadataHistory)
	mux.HandleFunc("/api/v1/metadata/history/revert/preview", s.handleMetadataHistoryRevertPreview)
	mux.HandleFunc("/api/v1/metadata/history/revert/apply", s.handleMetadataHistoryRevertApply)
	mux.HandleFunc("/api/v1/metadata/lock", s.handleMetadataLock)
	mux.HandleFunc("/api/v1/metadata/policy", s.handleMetadataPolicy)
	mux.HandleFunc("/api/v1/metadata/refresh", s.handleMetadataRefresh)
	mux.HandleFunc("/api/v1/metadata/nfo", s.handleMetadataNFO)
	mux.HandleFunc("/api/v1/metadata/analyze", s.handleMetadataAnalyze)
	mux.HandleFunc("/api/v1/metadata/replaygain", s.handleMetadataReplayGain)
	mux.HandleFunc("/api/v1/metadata/artwork", s.handleMetadataArtwork)
	mux.HandleFunc("/api/v1/metadata/similar", s.handleMetadataSimilar)
	mux.HandleFunc("/api/v1/metadata/writeback", s.handleMetadataWriteback)

	mux.HandleFunc("/api/v1/staging", s.handleStagingList)
	mux.HandleFunc("/api/v1/staging/apply", s.handleStagingApply)
	mux.HandleFunc("/api/v1/staging/ignore", s.handleStagingIgnore)
	mux.HandleFunc("/api/v1/staging/undo", s.handleStagingUndo)
	mux.HandleFunc("/api/v1/staging/clear-covers", s.handleStagingClearCovers)
	mux.HandleFunc("/api/v1/staging/clear-all", s.handleStagingClearAll)
	mux.HandleFunc("/api/v1/db/reset", s.handleDatabaseReset)

	mux.HandleFunc("/api/v1/tracks", s.handleTracksList)
	mux.HandleFunc("/api/v1/tracks/stream", s.handleTrackStream)
	mux.HandleFunc("/api/v1/tracks/waveform", s.handleTrackWaveform)
	mux.HandleFunc("/api/v1/tracks/tags", s.handleTrackTags)
	mux.HandleFunc("/api/v1/covers/local", s.handleLocalCover)
	mux.HandleFunc("/api/v1/covers/candidates", s.handleCoverCandidates)
	mux.HandleFunc("/api/v1/covers/ai-resolve", s.handleCoverAIResolve)
	mux.HandleFunc("/api/v1/navidrome/artist-image", s.handleNavidromeArtistImage)
	mux.HandleFunc("/api/v1/navidrome/album-cover", s.handleNavidromeAlbumCover)
	mux.HandleFunc("/api/v1/lyrics/cache", s.handleLyricsCache)
	mux.HandleFunc("/api/v1/lyrics/save", s.handleLyricsSave)
	mux.HandleFunc("/api/v1/lyrics/delete", s.handleLyricsDelete)
	mux.HandleFunc("/api/v1/lyrics/enqueue", s.handleLyricsEnqueue)
	mux.HandleFunc("/api/v1/jobs", s.handleJobsList)
	mux.HandleFunc("/api/v1/jobs/action", s.handleJobAction)

	mux.HandleFunc("/api/v1/keys", s.handleKeysAPI)
	mux.HandleFunc("/api/v1/cast", s.handleCast)
	mux.HandleFunc("/api/v1/public-access", s.handlePublicAccess)
	mux.HandleFunc("/api/v1/secure-transport", s.handleSecureTransport)
	mux.HandleFunc("/api/v1/secure-transport/qr.svg", s.handleSecureTransportQR)
	mux.HandleFunc("/api/v1/secure-transport/devices", s.handleSecureDevices)
	mux.HandleFunc("/api/v1/navidrome/account", s.handleNavidromeAccount)
	mux.HandleFunc("/api/v1/artist-biographies", s.handleArtistBiographies)
	mux.HandleFunc("/api/v1/artist-biographies/detail", s.handleArtistBiographyDetail)
	mux.HandleFunc("/api/v1/artist-biographies/reset", s.handleArtistBiographyReset)
	mux.HandleFunc("/api/v1/artist-biographies/rebuild", s.handleArtistBiographyRebuild)
	mux.HandleFunc("/api/v1/debug/fs", s.handleDebugFS)

	// Встроенный фронтенд (SPA) через //go:embed с антикэшированием для динамических скриптов и стилей
	uiSub, err := fs.Sub(embeddedUI, "ui")
	if err == nil {
		fileServer := http.FileServer(http.FS(uiSub))
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			// Адреса страниц (/library, /settings/keys) без расширения — это SPA: отдаём index.html, маршрут разбирает app.js.
			if p != "/" && path.Ext(p) == "" && !strings.HasPrefix(p, "/api/") {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
				p = "/"
			}
			if p == "/" || strings.HasSuffix(p, ".html") || strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") {
				w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
				w.Header().Set("Pragma", "no-cache")
				w.Header().Set("Expires", "0")
			}
			fileServer.ServeHTTP(w, r)
		}))
	}

	s.handler = security.AdminGuardMiddleware(mux)
	return s
}

// ServeHTTP реализует интерфейс http.Handler.
func (s *AdminServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	}
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

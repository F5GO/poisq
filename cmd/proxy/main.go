// Package main является точкой входа в микросервис poisq.
//
// Данный файл main.go инициализирует конфигурацию, подключается к базам данных SQLite
// (navidrome.db и собственной poisq.db), запускает фоновый воркер очереди текстов и AI-караоке,
// и поднимает два изолированных HTTP-сервера:
// 1. Публичный порт (:4534) — исключительно умный поиск и прозрачный Subsonic-прокси (lyrics отдает сам Navidrome).
// 2. Внутренний порт (:4535) — панель управления медиатекой, Staging-рефакторинг и менеджер LRC/API-ключей.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/F5GO/poisq/internal/admin"
	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/config"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/logger"
	"github.com/F5GO/poisq/internal/lyrics"
	"github.com/F5GO/poisq/internal/metadata"
	"github.com/F5GO/poisq/internal/proxy"
	"github.com/F5GO/poisq/internal/search"
	"github.com/F5GO/poisq/internal/security"
	"github.com/F5GO/poisq/internal/tagger"
	"github.com/F5GO/poisq/internal/worker"
)

func main() {
	log.Printf("[INFO] Запуск poisq v%s (Smart Search Proxy :4534 + AI Media Studio :4535)...", config.Version)

	// 1. Загрузка конфигурации
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[FATAL] Ошибка конфигурации: %v", err)
	}

	logger.SetLevel(cfg.LogLevel)
	logger.Info("Уровень логирования установлен: %s (LOG_LEVEL=%s)", logger.CurrentLevelStr(), cfg.LogLevel)

	logger.Info("Конфигурация: ProxyAddr=%s, AdminAddr=%s, NavidromeURL=%s, NavDB=%s, PoisqDB=%s, MusicPath=%s",
		cfg.ListenAddr, cfg.AdminListenAddr, cfg.NavidromeURL, cfg.NavidromeDBPath, cfg.PoisqDBPath, cfg.MusicPath)

	// Диагностика файловой системы точки монтирования MusicPath при старте
	if entries, err := os.ReadDir(cfg.MusicPath); err == nil {
		var subdirs []string
		for _, e := range entries {
			if e.IsDir() {
				subdirs = append(subdirs, e.Name())
			}
		}
		logger.Info("[MOUNT] Точка монтирования %s доступна, обнаружено каталогов первого уровня (%d): %v", cfg.MusicPath, len(subdirs), subdirs)
	} else {
		logger.Warn("[MOUNT] Точка монтирования %s недоступна для чтения: %v", cfg.MusicPath, err)
	}

	// 2. Инициализация словаря, поискового движка и менеджера алиасов
	dict := search.NewDictionary()
	engine := search.NewEngine(dict)
	aliasMgr := search.NewAliasManager(cfg.AliasesFile, cfg.AliasesSyncURL, dict)

	// 3. Инициализация собственной базы данных poisq.db и контуров безопасности
	poisqDB, err := db.OpenPoisqDatabase(cfg.PoisqDBPath)
	if err != nil {
		log.Fatalf("[FATAL] Не удалось открыть собственную БД poisq.db (%s): %v", cfg.PoisqDBPath, err)
	}
	defer poisqDB.Close()

	keyCipher, err := security.NewKeyCipher(cfg.SecretKey, cfg.PoisqDBPath)
	if err != nil {
		log.Fatalf("[FATAL] Ошибка инициализации шифрования ключей: %v", err)
	}

	_ = os.MkdirAll(cfg.MusicPath, 0755)
	pathGuard, err := security.NewPathGuard(cfg.MusicPath)
	if err != nil {
		log.Fatalf("[FATAL] Ошибка инициализации PathGuard для %s: %v", cfg.MusicPath, err)
	}

	// 4. Инициализация репозиториев и AI/LRC/Metadata сервисов
	keyRepo := db.NewKeyRepository(poisqDB)
	trackRepo := db.NewTrackRepository(poisqDB)
	stagingRepo := db.NewStagingRepository(poisqDB)
	jobRepo := db.NewJobRepository(poisqDB)
	cacheRepo := db.NewProviderCacheRepository(poisqDB)

	keyPoolMgr := keypool.NewManager(keyRepo, keyCipher)
	tournament := lyrics.NewTournamentOrchestrator(cacheRepo, keyPoolMgr)
	lrcWriter := lyrics.NewAtomicLRCWriter(pathGuard)

	geminiClient := ai.NewGeminiClient(keyPoolMgr)
	lyricsCleaner := ai.NewLyricsCleaner(geminiClient)
	metadataArbiter := ai.NewMetadataArbiter(geminiClient)
	audioTranscoder := ai.NewAudioTranscoder(pathGuard)
	groqWhisper := ai.NewGroqWhisperClient(keyPoolMgr)
	karaokePipeline := ai.NewKaraokePipeline(audioTranscoder, groqWhisper, geminiClient)

	mbClient := metadata.NewMusicBrainzClient()
	acoustIDClient := metadata.NewAcoustIDClient(pathGuard, keyPoolMgr)
	coverFetcher := metadata.NewCoverFetcher(pathGuard)
	libraryScanner := metadata.NewLibraryScanner(
		pathGuard.Root(), trackRepo, stagingRepo, mbClient, acoustIDClient, coverFetcher,
	)
	libraryScanner.SetArbiter(metadataArbiter)

	atomicTagger := tagger.NewAtomicTagWriter(pathGuard)
	stagingService := tagger.NewStagingService(atomicTagger, coverFetcher, trackRepo, stagingRepo)

	queueWorker := worker.NewQueueProcessor(
		jobRepo, trackRepo, cacheRepo, keyPoolMgr, tournament, lyricsCleaner, karaokePipeline, lrcWriter,
	)

	// 5. Быстрая загрузка поискового словаря из SQLite Navidrome (без блокировки старта серверов)
	if sqliteDB, err := db.OpenDatabase(cfg.NavidromeDBPath); err == nil {
		if items, err := db.LoadLibraryVocabulary(sqliteDB); err == nil {
			dict.Rebuild(items)
			log.Printf("[INFO] Поисковый словарь загружен на старте (%d уникальных слов)", dict.TotalWords())
		}
		_ = sqliteDB.Close()
	} else {
		log.Printf("[WARN] База данных Navidrome недоступна на старте: %v. Поиск будет работать в базовом режиме до появления БД", err)
	}

	// 6. Запуск Публичного HTTP Reverse Proxy (:4534 — ТОЛЬКО умный поиск)
	proxyServer := proxy.NewServer(cfg.NavidromeURL, engine, dict, aliasMgr, cfg.EnableDebugAPI)
	publicHTTPServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           proxyServer,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("[INFO] Публичный Subsonic-прокси (Умный поиск) слушает %s", cfg.ListenAddr)
		if err := publicHTTPServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[FATAL] Ошибка публичного HTTP-сервера: %v", err)
		}
	}()

	// 7. Мгновенный запуск Внутреннего HTTP-сервера панели управления (:4535)
	adminHandler := admin.NewAdminServer(
		trackRepo, stagingRepo, jobRepo, cacheRepo, keyPoolMgr,
		libraryScanner, stagingService, lrcWriter, queueWorker,
		pathGuard,
		func() (*sql.DB, error) { return db.OpenDatabase(cfg.NavidromeDBPath) },
	)
	adminHandler.SetMetadataArbiter(metadataArbiter)
	adminHandler.SetPoisqDB(poisqDB)
	if cfg.NavidromeURL != nil {
		adminHandler.SetNavidromeURL(cfg.NavidromeURL.String())
	}
	adminHTTPServer := &http.Server{
		Addr:              cfg.AdminListenAddr,
		Handler:           adminHandler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("[INFO] Панель управления poisq Studio слушает %s", cfg.AdminListenAddr)
		if err := adminHTTPServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[FATAL] Ошибка административного HTTP-сервера: %v", err)
		}
	}()

	// 8. Запуск фоновых процессов (воркер очереди LRC/Karaoke, асинхронная синхронизация каталога и алиасов)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	queueWorker.Start(ctx)
	aliasMgr.StartDailySync(ctx)

	// Первичная фоновая синхронизация и периодический цикл обновления
	go func() {
		if sqliteDB, err := db.OpenDatabase(cfg.NavidromeDBPath); err == nil {
			enricher := db.NewEnricher(sqliteDB, cfg.EnrichSQLiteFullText)
			if err := enricher.InitSchema(); err != nil {
				log.Printf("[WARN] Не удалось инициализировать схему обогатителя: %v", err)
			}
			syncData(sqliteDB, dict, enricher, libraryScanner, cfg)
			_ = sqliteDB.Close()
		}
		runBackgroundSync(ctx, cfg, dict, libraryScanner)
	}()

	// 9. Ожидание сигнала завершения работы
	<-ctx.Done()
	log.Println("[INFO] Получен сигнал завершения. Остановка серверов...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = publicHTTPServer.Shutdown(shutdownCtx)
	_ = adminHTTPServer.Shutdown(shutdownCtx)

	log.Println("[INFO] Сервис poisq успешно остановлен")
}

// syncData вычитывает свежие данные из базы Navidrome, обновляет поисковый словарь,
// запускает обогащение full_text и синхронизирует список треков в poisq.db.
func syncData(sqliteDB *sql.DB, dict *search.Dictionary, enricher *db.Enricher, scanner *metadata.LibraryScanner, cfg *config.Config) {
	if sqliteDB == nil {
		return
	}

	items, err := db.LoadLibraryVocabulary(sqliteDB)
	if err != nil {
		log.Printf("[WARN] Ошибка загрузки словаря: %v", err)
	} else {
		dict.Rebuild(items)
		log.Printf("[INFO] Словарь успешно обновлен. Всего уникальных слов: %d", dict.TotalWords())
	}

	if enricher != nil && cfg.EnrichSQLiteFullText {
		enricher.RunEnrichment()
	}

	if scanner != nil {
		if synced, err := scanner.SyncFromNavidromeDB(sqliteDB); err == nil && synced > 0 {
			log.Printf("[INFO] Каталог poisq.db синхронизирован с Navidrome (%d треков)", synced)
		}
	}
}

// runBackgroundSync выполняет периодическое обновление словаря, обогащение базы и синхронизацию треков.
func runBackgroundSync(ctx context.Context, cfg *config.Config, dict *search.Dictionary, scanner *metadata.LibraryScanner) {
	ticker := time.NewTicker(cfg.SyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sqliteDB, err := db.OpenDatabase(cfg.NavidromeDBPath)
			if err != nil {
				log.Printf("[WARN] Фоновое переподключение к БД Navidrome не удалось: %v", err)
				continue
			}

			enricher := db.NewEnricher(sqliteDB, cfg.EnrichSQLiteFullText)
			_ = enricher.InitSchema()

			syncData(sqliteDB, dict, enricher, scanner, cfg)
			_ = sqliteDB.Close()
		}
	}
}

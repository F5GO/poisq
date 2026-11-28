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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/F5GO/poisq/internal/admin"
	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/aligner"
	"github.com/F5GO/poisq/internal/artistbio"
	"github.com/F5GO/poisq/internal/cast"
	"github.com/F5GO/poisq/internal/config"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/logger"
	"github.com/F5GO/poisq/internal/lyrics"
	"github.com/F5GO/poisq/internal/metadata"
	"github.com/F5GO/poisq/internal/proxy"
	"github.com/F5GO/poisq/internal/search"
	"github.com/F5GO/poisq/internal/securetransport"
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
	metadataRepo := db.NewMetadataRepository(poisqDB)
	mediaInfoRepo := db.NewMediaInfoRepository(poisqDB)
	metadataPolicyRepo := db.NewMetadataPolicyRepository(poisqDB)
	audioAnalysisRepo := db.NewAudioAnalysisRepository(poisqDB)
	mediaEnrichmentRepo := db.NewMediaEnrichmentRepository(poisqDB)

	// Автоматическая дедупликация базы фонотеки при запуске
	if del, err := trackRepo.DeduplicateTracks(); err == nil && del > 0 {
		log.Printf("[INFO] [DEDUP] Выполнена дедупликация базы фонотеки: удалено %d дубликатов", del)
	}

	keyPoolMgr := keypool.NewManager(keyRepo, keyCipher)
	tournament := lyrics.NewTournamentOrchestrator(cacheRepo, keyPoolMgr)
	lrcWriter := lyrics.NewAtomicLRCWriter(pathGuard)
	lrcWriter.SetResolver(metadata.FindAudioFileAnywhere)

	geminiClient := ai.NewGeminiClient(keyPoolMgr)
	groqChat := ai.NewGroqChatClient(keyPoolMgr)
	// Текстовые задачи сначала идут в быстрый Groq (Llama), Gemini — запасной (часто отвечает 503 High Demand).
	// Порядок можно поменять: LLM_PRIMARY=gemini.
	llm := ai.NewFallbackGenerator()
	if strings.EqualFold(os.Getenv("LLM_PRIMARY"), "gemini") {
		llm.Add("gemini", geminiClient).Add("groq", groqChat)
	} else {
		llm.Add("groq", groqChat).Add("gemini", geminiClient)
	}
	lyricsCleaner := ai.NewLyricsCleaner(llm)
	metadataArbiter := ai.NewMetadataArbiter(llm)
	audioTranscoder := ai.NewAudioTranscoder(pathGuard)
	groqWhisper := ai.NewGroqWhisperClient(keyPoolMgr)
	karaokePipeline := ai.NewKaraokePipeline(audioTranscoder, groqWhisper, llm)
	tournament.SetQueryExpander(ai.NewLLMQueryExpander(llm, db.NewAliasCacheRepository(poisqDB)))
	artistJudge := ai.NewLLMArtistJudge(llm)
	tournament.SetArtistJudge(artistJudge)

	mbClient := metadata.NewMusicBrainzClient()
	tournament.SetArtistAliasSource(mbClient)
	bioStore, err := artistbio.NewStore(poisqDB)
	if err != nil {
		log.Fatalf("[FATAL] Не удалось инициализировать хранилище биографий: %v", err)
	}
	bioSources, err := artistbio.NewDefaultBiographySources(mbClient, keyPoolMgr)
	if err != nil {
		log.Fatalf("[FATAL] Не удалось инициализировать источники биографий: %v", err)
	}
	bioIngestion, err := artistbio.NewIngestionService(bioStore, bioSources, 8)
	if err != nil {
		log.Fatalf("[FATAL] Не удалось инициализировать сбор биографий: %v", err)
	}
	bioIngestion.SetArtistLinks(mbClient)
	var regionalIdentitySource artistbio.IdentityCandidateSource
	for _, source := range bioSources {
		if source.Provider() == artistbio.ProviderYandexMusic {
			regionalIdentitySource, _ = source.(artistbio.IdentityCandidateSource)
			break
		}
	}
	identitySource := artistbio.NewFallbackIdentitySource(artistbio.NewMusicBrainzIdentitySource(mbClient), regionalIdentitySource)
	identityResolver := artistbio.NewIdentityResolver(bioStore, trackRepo, identitySource, artistJudge)
	bioValidator, err := artistbio.NewBiographyValidator(bioStore, ai.NewBiographyAI(llm), 4)
	if err != nil {
		log.Fatalf("[FATAL] Не удалось инициализировать проверку биографий: %v", err)
	}
	tavilySearch, err := artistbio.NewTavilySearch(keyPoolMgr)
	if err != nil {
		log.Fatalf("[FATAL] Не удалось инициализировать поиск источников биографий: %v", err)
	}
	bioPipeline, err := artistbio.NewSynthesisPipeline(bioStore, trackRepo, identityResolver, bioIngestion, bioValidator, tavilySearch, ai.NewBiographySynthesisAI(llm))
	if err != nil {
		log.Fatalf("[FATAL] Не удалось инициализировать синтез биографий: %v", err)
	}
	bioCoordinator, err := artistbio.NewCoordinator(bioStore, func() (*sql.DB, error) { return db.OpenDatabase(cfg.NavidromeDBPath) }, bioPipeline)
	if err != nil {
		log.Fatalf("[FATAL] Не удалось инициализировать очередь биографий: %v", err)
	}
	coverFetcher := metadata.NewCoverFetcher(pathGuard)
	libraryScanner := metadata.NewLibraryScanner(
		pathGuard.Root(), trackRepo, stagingRepo, mbClient, coverFetcher,
	)
	libraryScanner.SetArbiter(metadataArbiter)
	localImporter := metadata.NewLocalImporter(pathGuard, metadataRepo, mediaInfoRepo, metadata.NewFFProbeAudioProber(""), 2)
	localImporter.SetPolicyProvider(metadataPolicyRepo.Get)
	localImporter.SetAnalysisRepository(audioAnalysisRepo)
	libraryScanner.SetLocalImporter(localImporter)

	atomicTagger := tagger.NewAtomicTagWriter(pathGuard)
	stagingService := tagger.NewStagingService(atomicTagger, coverFetcher, trackRepo, stagingRepo)
	nfoService := metadata.NewNFOService(pathGuard, trackRepo, metadataRepo, metadataPolicyRepo, stagingRepo)
	audioAnalysisService := metadata.NewAudioAnalysisService(pathGuard, trackRepo, audioAnalysisRepo, stagingRepo, metadata.NewFFmpegLoudnessAnalyzer(""), 2)
	mediaEnrichmentService := metadata.NewMediaEnrichmentService(pathGuard, stagingRepo, mediaEnrichmentRepo, metadata.NewListenBrainzClient())
	if err := stagingService.RecoverIncomplete(context.Background()); err != nil {
		log.Fatalf("[FATAL] Не удалось восстановить незавершённую транзакцию метаданных: %v", err)
	}

	queueWorker := worker.NewQueueProcessor(
		jobRepo, trackRepo, cacheRepo, keyPoolMgr, tournament, lyricsCleaner, karaokePipeline, lrcWriter,
	)

	confidenceRepo := db.NewConfidenceRepository(poisqDB)
	var alignerClient *aligner.Client
	if cfg.AlignerURL != "" {
		alignerClient = aligner.NewClient(cfg.AlignerURL, cfg.AlignerToken)
		queueWorker.SetAligner(alignerClient, confidenceRepo, pathGuard.Root(), cfg.AlignerMusicPath)
		log.Printf("[INFO] Акустическое выравнивание включено: сервис %s (проверка связи выполняется при первой задаче)", cfg.AlignerURL)
	} else {
		log.Printf("[INFO] ALIGNER_URL не задан: караоке-тайминг строится облачным режимом Groq Whisper + Gemini")
	}

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
	// Трансляция на колонки через Music Assistant: адрес и колонки — в Studio, токен — в пуле ключей
	castService := &cast.Service{
		DB:        poisqDB,
		OpenNavDB: func() (*sql.DB, error) { return db.OpenDatabase(cfg.NavidromeDBPath) },
		Token: func() (string, error) {
			k, err := keyPoolMgr.Acquire(cast.TokenProvider)
			if err != nil {
				return "", err
			}
			return k.PlainKey, nil
		},
		HTTP: &http.Client{Timeout: 10 * time.Second},
	}

	proxyServer := proxy.NewServer(cfg.NavidromeURL, engine, dict, aliasMgr, cfg.EnableDebugAPI)
	proxyServer.SetTrustedProxies(cfg.TrustedProxies)
	proxyServer.SetCast(castService)
	// Веб-интерфейс Navidrome на :4534 закрыт по умолчанию и при ошибке чтения настройки.
	publicAccess := &security.PublicAccess{}
	if err := admin.LoadPublicAccess(poisqDB, publicAccess); err != nil {
		log.Printf("[WARN] Политика публичного входа не прочитана, веб-интерфейс Navidrome закрыт: %v", err)
	}
	proxyServer.SetPublicAccess(publicAccess)
	// Secure Transport выключен по умолчанию; ключи установки создаются при первом включении рядом с базой poisq.
	secureTransport := securetransport.NewInstallation(filepath.Join(filepath.Dir(cfg.PoisqDBPath), "secure-transport"))
	if err := admin.LoadSecureTransport(poisqDB, secureTransport, time.Now()); err != nil {
		log.Printf("[WARN] Настройки Secure Transport не применены, функция выключена: %v", err)
	}
	// Subsonic через канал идёт в тот же обработчик прокси внутри процесса: умный поиск и расширения poisq работают как обычно.
	secureServer := securetransport.NewServer(secureTransport, securetransport.NewSubsonicHandler(proxyServer), proxy.ExtractClientIP)
	// Вход по каналу: пароль проверяет Navidrome, Studio хранит устройства с паролем, зашифрованным мастер-ключом.
	secureAccounts := securetransport.NewAccounts(
		securetransport.NewDeviceStore(poisqDB, keyCipher),
		securetransport.NewNavidromeVerifier(cfg.NavidromeURL, nil),
	)
	secureServer.SetAccounts(secureAccounts)
	secureServer.SetUpstream(proxyServer)
	proxyServer.SetSecureTransport(secureServer)
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for now := range ticker.C {
			if err := secureTransport.Rotate(now); err != nil {
				log.Printf("[ERROR] Ротация рабочего ключа Secure Transport: %v", err)
			}
		}
	}()
	proxyServer.SetBiographyReader(bioStore)
	proxyServer.SetBiographyCoordinator(bioCoordinator, cfg.BiographyFirstRequestWait)
	publicHTTPServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           proxyServer,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	var lanHTTPServer *http.Server
	if cfg.LANListenAddr != "" {
		lanHTTPServer = &http.Server{
			Addr:              cfg.LANListenAddr,
			Handler:           proxyServer.LANHandler(),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		go func() {
			log.Printf("[INFO] Домашний Subsonic-прокси без шифрования слушает %s (не пробрасывайте его через FRP)", cfg.LANListenAddr)
			if err := lanHTTPServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("[FATAL] Ошибка домашнего HTTP-сервера: %v", err)
			}
		}()
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
	adminHandler.SetConfidenceRepository(confidenceRepo)
	adminHandler.SetAlignerClient(alignerClient)
	adminHandler.SetCast(castService)
	adminHandler.SetPublicAccess(publicAccess)
	adminHandler.SetSecureTransport(secureTransport)
	adminHandler.SetSecureAccounts(secureAccounts)
	adminHandler.SetBiographyStore(bioStore)
	adminHandler.SetBiographyCoordinator(bioCoordinator)
	adminHandler.SetMetadataControlServices(admin.MetadataControlServices{
		Metadata: metadataRepo, MediaInfo: mediaInfoRepo, Policy: metadataPolicyRepo,
		Analysis: audioAnalysisRepo, Writebacks: stagingRepo.WritebackRepository(), History: db.NewMetadataHistoryRepository(poisqDB), AudioService: audioAnalysisService,
		NFOService: nfoService, Enrichment: mediaEnrichmentRepo, EnrichmentSvc: mediaEnrichmentService,
	})
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
	bioCoordinator.Start(ctx)

	// Первичная фоновая синхронизация и периодический цикл обновления
	var syncWG sync.WaitGroup
	syncWG.Add(1)
	go func() {
		defer syncWG.Done()
		if sqliteDB, err := db.OpenDatabase(cfg.NavidromeDBPath); err == nil {
			if n, err := db.RemoveLegacyEnrichment(sqliteDB); err != nil {
				log.Printf("[WARN] Не удалось убрать прежнее обогащение full_text из navidrome.db: %v", err)
			} else if n > 0 {
				log.Printf("[INFO] Из navidrome.db убраны вставки poisq в full_text (%d записей)", n)
			}
			syncData(sqliteDB, dict, libraryScanner)
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
	if lanHTTPServer != nil {
		_ = lanHTTPServer.Shutdown(shutdownCtx)
	}
	_ = adminHTTPServer.Shutdown(shutdownCtx)
	syncWG.Wait()
	_ = castService.Close()

	log.Println("[INFO] Сервис poisq успешно остановлен")
}

// syncData вычитывает свежие данные из базы Navidrome (только чтение), обновляет поисковый словарь
// и синхронизирует список треков в poisq.db.
func syncData(sqliteDB *sql.DB, dict *search.Dictionary, scanner *metadata.LibraryScanner) {
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

	if scanner != nil {
		if synced, err := scanner.SyncFromNavidromeDB(sqliteDB); err == nil && synced > 0 {
			log.Printf("[INFO] Каталог poisq.db синхронизирован с Navidrome (%d треков)", synced)
		}
	}

}

// runBackgroundSync выполняет периодическое обновление словаря и синхронизацию треков.
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

			syncData(sqliteDB, dict, scanner)
			_ = sqliteDB.Close()
		}
	}
}

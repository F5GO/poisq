// Package main является точкой входа в микросервис poisq.
//
// Данный файл main.go инициализирует конфигурацию, подключение к SQLite базе данных Navidrome,
// строит оперативный словарь поиска, запускает периодическое фоновое обогащение full_text,
// поднимает HTTP-сервер реверс-прокси и обеспечивает корректное завершение (graceful shutdown).
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

	"github.com/F5GO/poisq/internal/config"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/proxy"
	"github.com/F5GO/poisq/internal/search"
)

func main() {
	log.Printf("[INFO] Запуск poisq v%s (Reverse Proxy & SQLite Enricher для Navidrome)...", config.Version)

	// 1. Загрузка конфигурации
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[FATAL] Ошибка конфигурации: %v", err)
	}

	log.Printf("[INFO] Конфигурация: ListenAddr=%s, NavidromeURL=%s, DB=%s, SyncInterval=%v, Enrich=%v",
		cfg.ListenAddr, cfg.NavidromeURL, cfg.NavidromeDBPath, cfg.SyncInterval, cfg.EnrichSQLiteFullText)

	// 2. Инициализация словаря, поискового движка и менеджера алиасов
	dict := search.NewDictionary()
	engine := search.NewEngine(dict)
	aliasMgr := search.NewAliasManager(cfg.AliasesFile, cfg.AliasesSyncURL, dict)

	// 3. Подключение к SQLite Navidrome
	var sqliteDB *sql.DB
	sqliteDB, err = db.OpenDatabase(cfg.NavidromeDBPath)
	if err != nil {
		log.Printf("[WARN] База данных недоступна на старте: %v. Поиск будет работать в базовом режиме до появления БД", err)
	} else {
		defer sqliteDB.Close()
	}

	// 4. Инициализация обогатителя
	var enricher *db.Enricher
	if sqliteDB != nil {
		enricher = db.NewEnricher(sqliteDB, cfg.EnrichSQLiteFullText)
		if err := enricher.InitSchema(); err != nil {
			log.Printf("[WARN] Не удалось инициализировать схему обогатителя: %v", err)
		}
	}

	// 5. Первичная синхронизация словаря и обогащение
	syncData(sqliteDB, dict, enricher, cfg)

	// 6. Фоновый цикл синхронизации по тикеру
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go runBackgroundSync(ctx, cfg, dict)
	aliasMgr.StartDailySync(ctx)

	// 7. Создание и запуск HTTP Reverse Proxy
	proxyServer := proxy.NewServer(cfg.NavidromeURL, engine, dict, aliasMgr, cfg.EnableDebugAPI)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           proxyServer,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("[INFO] Реверс-прокси успешно слушает %s", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[FATAL] Ошибка HTTP-сервера: %v", err)
		}
	}()

	// 8. Ожидание сигнала завершения работы
	<-ctx.Done()
	log.Println("[INFO] Получен сигнал завершения. Остановка сервера...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("[ERROR] Ошибка при graceful shutdown: %v", err)
	}

	log.Println("[INFO] Сервис poisq успешно остановлен")
}

// syncData вычитывает свежие данные из базы и запускает обогащение full_text.
func syncData(sqliteDB *sql.DB, dict *search.Dictionary, enricher *db.Enricher, cfg *config.Config) {
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
}

// runBackgroundSync выполняет периодическое обновление словаря и обогащение базы.
func runBackgroundSync(ctx context.Context, cfg *config.Config, dict *search.Dictionary) {
	ticker := time.NewTicker(cfg.SyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Если соединение еще не было открыто, пробуем открыть
			sqliteDB, err := db.OpenDatabase(cfg.NavidromeDBPath)
			if err != nil {
				log.Printf("[WARN] Фоновое переподключение к БД не удалось: %v", err)
				continue
			}

			enricher := db.NewEnricher(sqliteDB, cfg.EnrichSQLiteFullText)
			_ = enricher.InitSchema()

			syncData(sqliteDB, dict, enricher, cfg)
			_ = sqliteDB.Close()
		}
	}
}

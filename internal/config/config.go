// Package config отвечает за загрузку, валидацию и предоставление параметров
// конфигурации сервиса poisq из переменных окружения.
// Код обеспечивает централизованное управление настройками и значениями по умолчанию.
package config

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/lyrics"
)

// Version определяет текущую версию микросервиса poisq.
const Version = "0.0.6"

// Config содержит все настраиваемые параметры микросервиса poisq.
type Config struct {
	// ListenAddr — адрес и порт, на котором прокси принимает входящие соединения (по умолчанию ":4534").
	ListenAddr string

	// NavidromeURL — URL вышестоящего инстанса Navidrome (по умолчанию "http://navidrome:4533").
	NavidromeURL *url.URL

	// NavidromeDBPath — путь к файлу SQLite базы данных Navidrome (по умолчанию "/data/navidrome.db").
	NavidromeDBPath string

	// SyncInterval — интервал периодической синхронизации словаря и обогащения SQLite (по умолчанию 2m).
	SyncInterval time.Duration

	// EnrichSQLiteFullText — флаг включения безопасного обогащения столбца full_text в SQLite.
	EnrichSQLiteFullText bool

	// LogLevel — уровень логирования ("debug", "info", "warn").
	LogLevel string

	// AliasesFile — локальный путь к файлу aliases.json (по умолчанию "/data/aliases.json").
	AliasesFile string

	// AliasesSyncURL — URL удаленного файла aliases.json на GitHub для ежедневного автообновления.
	AliasesSyncURL string

	// EnableDebugAPI — флаг включения диагностических эндпоинтов /_smart/* (по умолчанию false для безопасности).
	EnableDebugAPI bool

	// Lyrics — параметры подсистемы поиска и кэширования текстов песен.
	Lyrics *lyrics.Config
}

// Load считывает конфигурацию из переменных окружения или устанавливает значения по умолчанию.
func Load() (*Config, error) {
	listenAddr := getEnv("LISTEN_ADDR", ":4534")
	rawNavidromeURL := getEnv("NAVIDROME_URL", "http://navidrome:4533")
	dbPath := getEnv("NAVIDROME_DB_PATH", "/data/navidrome.db")
	rawSyncInterval := getEnv("SYNC_INTERVAL", "2m")
	rawEnrich := getEnv("ENRICH_SQLITE_FULLTEXT", "true")
	logLevel := strings.ToLower(getEnv("LOG_LEVEL", "info"))
	aliasesFile := getEnv("ALIASES_FILE", "/data/aliases.json")
	aliasesSyncURL := getEnv("ALIASES_SYNC_URL", "https://raw.githubusercontent.com/F5GO/poisq/main/aliases.json")
	rawDebugAPI := getEnv("ENABLE_DEBUG_API", "false")

	parsedURL, err := url.Parse(rawNavidromeURL)
	if err != nil {
		return nil, fmt.Errorf("некорректный NAVIDROME_URL %q: %w", rawNavidromeURL, err)
	}

	syncInterval, err := time.ParseDuration(rawSyncInterval)
	if err != nil {
		log.Printf("[WARN] Некорректный SYNC_INTERVAL %q, используется 2m: %v", rawSyncInterval, err)
		syncInterval = 2 * time.Minute
	}

	enrichFullText, err := strconv.ParseBool(rawEnrich)
	if err != nil {
		enrichFullText = true
	}

	enableDebugAPI, err := strconv.ParseBool(rawDebugAPI)
	if err != nil {
		enableDebugAPI = false
	}

	lyricsCfg := lyrics.LoadConfig()

	cfg := &Config{
		ListenAddr:           listenAddr,
		NavidromeURL:         parsedURL,
		NavidromeDBPath:      dbPath,
		SyncInterval:         syncInterval,
		EnrichSQLiteFullText: enrichFullText,
		LogLevel:             logLevel,
		AliasesFile:          aliasesFile,
		AliasesSyncURL:       aliasesSyncURL,
		EnableDebugAPI:       enableDebugAPI,
		Lyrics:               lyricsCfg,
	}

	return cfg, nil
}

// getEnv возвращает значение переменной окружения или defaultValue, если она не установлена.
func getEnv(key, defaultValue string) string {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return defaultValue
}

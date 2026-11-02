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
)

// Version определяет текущую версию микросервиса poisq.
const Version = "0.2.0"

// Config содержит все настраиваемые параметры микросервиса poisq.
type Config struct {
	// ListenAddr — публичный адрес и порт Subsonic-прокси (только умный поиск, по умолчанию ":4534").
	ListenAddr string

	// AdminListenAddr — внутренний адрес и порт панели управления и AI-комбайна (по умолчанию ":4535").
	AdminListenAddr string

	// NavidromeURL — URL вышестоящего инстанса Navidrome (по умолчанию "http://navidrome:4533").
	NavidromeURL *url.URL

	// NavidromeDBPath — путь к файлу SQLite базы данных Navidrome (по умолчанию "/data/navidrome.db").
	NavidromeDBPath string

	// PoisqDBPath — путь к собственной SQLite базе данных poisq.db (по умолчанию "/data/poisq.db").
	PoisqDBPath string

	// MusicPath — корневая директория медиатеки с правами на чтение и запись (по умолчанию "/music").
	MusicPath string

	// SecretKey — мастер-ключ для шифрования API-ключей в БД (AES-256-GCM).
	SecretKey string

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
}

// Load считывает конфигурацию из переменных окружения или устанавливает значения по умолчанию.
func Load() (*Config, error) {
	listenAddr := getEnv("LISTEN_ADDR", ":4534")
	adminListenAddr := getEnv("ADMIN_LISTEN_ADDR", ":4535")
	rawNavidromeURL := getEnv("NAVIDROME_URL", "http://navidrome:4533")
	dbPath := getEnv("NAVIDROME_DB_PATH", "/data/navidrome.db")
	poisqDBPath := getEnv("POISQ_DB_PATH", "/data/poisq.db")
	musicPath := getEnv("MUSIC_PATH", "/music")
	secretKey := getEnv("POISQ_SECRET_KEY", "")
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

	cfg := &Config{
		ListenAddr:           listenAddr,
		AdminListenAddr:      adminListenAddr,
		NavidromeURL:         parsedURL,
		NavidromeDBPath:      dbPath,
		PoisqDBPath:          poisqDBPath,
		MusicPath:            musicPath,
		SecretKey:            secretKey,
		SyncInterval:         syncInterval,
		EnrichSQLiteFullText: enrichFullText,
		LogLevel:             logLevel,
		AliasesFile:          aliasesFile,
		AliasesSyncURL:       aliasesSyncURL,
		EnableDebugAPI:       enableDebugAPI,
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

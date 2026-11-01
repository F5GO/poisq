// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл config.go инкапсулирует параметры конфигурации модуля текстов песен,
// их парсинг из переменных окружения и установку безопасных значений по умолчанию.
package lyrics

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config содержит настройки подсистемы Lyrics.
type Config struct {
	// Enabled определяет, включен ли перехват и поиск текстов песен.
	Enabled bool

	// SaveToDisk определяет, разрешена ли запись .lrc файлов на диск в папку с музыкой (по умолчанию false).
	SaveToDisk bool

	// MusicPath — путь к корневой директории с музыкой для сохранения .lrc (по умолчанию "/music").
	MusicPath string

	// Providers — упорядоченный список идентификаторов провайдеров (например: "yandex,genius,musixmatch,lrclib,amalgama").
	Providers []string

	// CustomSources — список внешних URL, совместимых с протоколами LRCLIB/lrcmux (добавление вручную).
	CustomSources []string

	// CacheTTL — время жизни закэшированного текста (по умолчанию 720 часов = 30 дней).
	CacheTTL time.Duration

	// CacheMaxEntries — максимальное количество текстов в оперативной памяти (по умолчанию 5000).
	CacheMaxEntries int

	// RequestTimeout — таймаут ожидания ответа от отдельного внешнего источника (по умолчанию 3 секунды).
	RequestTimeout time.Duration

	// DurationTolerance — допустимое отклонение длительности трека при сопоставлении с базой (по умолчанию 4 секунды).
	DurationTolerance time.Duration

	// MaxConcurrentRequests — максимальное число одновременных исходящих запросов к провайдерам (защита от исчерпания сокетов).
	MaxConcurrentRequests int

	// APIToken — опциональный секретный токен для доступа к эндпоинтам /lyrics/api/* из внешней сети.
	APIToken string

	// EnablePublicAPI — разрешен ли публичный доступ к /lyrics/api/* без токена из внешнего интернета (по умолчанию false).
	EnablePublicAPI bool
}

// LoadConfig считывает параметры подсистемы lyrics из переменных окружения.
func LoadConfig() *Config {
	enabled := getEnvBool("LYRICS_ENABLED", true)
	saveToDisk := getEnvBool("LYRICS_SAVE_TO_DISK", false)
	musicPath := getEnvString("LYRICS_MUSIC_PATH", "/music")
	providersStr := getEnvString("LYRICS_PROVIDERS", "yandex,genius,musixmatch,lrclib,amalgama")
	customSourcesStr := getEnvString("LYRICS_CUSTOM_SOURCES", "")
	cacheTTLStr := getEnvString("LYRICS_CACHE_TTL", "720h")
	cacheMaxEntries := getEnvInt("LYRICS_CACHE_MAX_ENTRIES", 5000)
	timeoutStr := getEnvString("LYRICS_TIMEOUT", "3s")
	toleranceStr := getEnvString("LYRICS_DURATION_TOLERANCE", "4s")
	maxConcurrent := getEnvInt("LYRICS_MAX_CONCURRENT_REQUESTS", 8)
	apiToken := getEnvString("LYRICS_API_TOKEN", "")
	enablePublicAPI := getEnvBool("LYRICS_ENABLE_PUBLIC_API", false)

	var providers []string
	for _, p := range strings.Split(providersStr, ",") {
		trimmed := strings.ToLower(strings.TrimSpace(p))
		if trimmed != "" {
			providers = append(providers, trimmed)
		}
	}

	var customSources []string
	if customSourcesStr != "" {
		for _, s := range strings.Split(customSourcesStr, ",") {
			trimmed := strings.TrimSpace(s)
			if trimmed != "" {
				customSources = append(customSources, trimmed)
			}
		}
	}

	cacheTTL, err := time.ParseDuration(cacheTTLStr)
	if err != nil {
		cacheTTL = 720 * time.Hour
	}

	reqTimeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		reqTimeout = 3 * time.Second
	}

	tolerance, err := time.ParseDuration(toleranceStr)
	if err != nil {
		tolerance = 4 * time.Second
	}

	return &Config{
		Enabled:               enabled,
		SaveToDisk:            saveToDisk,
		MusicPath:             musicPath,
		Providers:             providers,
		CustomSources:         customSources,
		CacheTTL:              cacheTTL,
		CacheMaxEntries:       cacheMaxEntries,
		RequestTimeout:        reqTimeout,
		DurationTolerance:     tolerance,
		MaxConcurrentRequests: maxConcurrent,
		APIToken:              apiToken,
		EnablePublicAPI:       enablePublicAPI,
	}
}

func getEnvInt(key string, defaultValue int) int {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		if parsed, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultValue
}

func getEnvString(key, defaultValue string) string {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(val)); err == nil {
			return parsed
		}
	}
	return defaultValue
}

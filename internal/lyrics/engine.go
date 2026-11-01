// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл engine.go реализует основной диспетчер поиска текстов: каскадный опрос провайдеров
// по приоритету (с предпочтением синхронизированного LRC), дедупликацию через SingleFlight и кэширование.
package lyrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
)

// Engine координирует работу кэша, провайдеров и сохранение текстов песен.
type Engine struct {
	cfg       *Config
	cache     *Cache
	writer    *DiskWriter
	providers []Provider
	flight    *SingleFlightGroup
	sem       chan struct{}
}

// NewEngine создает и инициализирует новый движок поиска текстов.
func NewEngine(cfg *Config, db *sql.DB) *Engine {
	cache := NewCache(db, cfg.CacheTTL, cfg.CacheMaxEntries)
	writer := NewDiskWriter(cfg.SaveToDisk, cfg.MusicPath, db)

	maxConcurrent := cfg.MaxConcurrentRequests
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}

	eng := &Engine{
		cfg:    cfg,
		cache:  cache,
		writer: writer,
		flight: NewSingleFlightGroup(),
		sem:    make(chan struct{}, maxConcurrent),
	}

	eng.initProviders()
	return eng
}

func (e *Engine) initProviders() {
	var list []Provider

	// 1. Сначала регистрируем кастомные URL пользователя (наивысший приоритет ручных источников)
	for i, customURL := range e.cfg.CustomSources {
		id := fmt.Sprintf("custom_%d", i+1)
		name := fmt.Sprintf("Custom Source %d (%s)", i+1, customURL)
		p := NewCustomLrclibProvider(id, name, customURL, e.cfg.RequestTimeout, e.cfg.DurationTolerance)
		list = append(list, p)
		log.Printf("[INFO] [LYRICS] Зарегистрирован кастомный источник текстов: %s", customURL)
	}

	// 2. Добавляем сконфигурированные стандартные провайдеры
	for _, pID := range e.cfg.Providers {
		switch strings.ToLower(pID) {
		case "yandex":
			list = append(list, NewYandexProvider(e.cfg.RequestTimeout, e.cfg.DurationTolerance))
		case "genius":
			list = append(list, NewGeniusProvider(e.cfg.RequestTimeout))
		case "musixmatch":
			list = append(list, NewMusixmatchProvider(e.cfg.RequestTimeout))
		case "lrclib":
			list = append(list, NewLrclibProvider(e.cfg.RequestTimeout, e.cfg.DurationTolerance))
		case "amalgama":
			list = append(list, NewAmalgamaProvider(e.cfg.RequestTimeout))
		default:
			log.Printf("[WARN] [LYRICS] Неизвестный идентификатор провайдера: %q", pID)
		}
	}

	e.providers = list
	log.Printf("[INFO] [LYRICS] Инициализировано %d источников текстов песен", len(e.providers))
}

// GetProviders возвращает список активных источников текстов.
func (e *Engine) GetProviders() []Provider {
	return e.providers
}

// GetCache возвращает двухуровневый кэш текстов.
func (e *Engine) GetCache() *Cache {
	return e.cache
}

// GetDB возвращает подключение к базе данных Navidrome (если доступно).
func (e *Engine) GetDB() *sql.DB {
	if e.writer != nil {
		return e.writer.db
	}
	return nil
}

// FindLyrics осуществляет поиск текста песни с проверкой кэша и каскадным опросом источников.
func (e *Engine) FindLyrics(ctx context.Context, q SearchQuery) (*Lyrics, error) {
	if !e.cfg.Enabled {
		return nil, ErrNotFound
	}

	artist := strings.TrimSpace(q.Artist)
	title := strings.TrimSpace(q.Title)
	if artist == "" || title == "" {
		return nil, errors.New("artist and title are required for lyrics search")
	}

	// Безопасность: ограничение длины (DoS-предотвращение) и очистка от управляющих символов
	artist = sanitizeInput(artist)
	title = sanitizeInput(title)
	if len([]rune(artist)) > 256 {
		artist = string([]rune(artist)[:256])
	}
	if len([]rune(title)) > 256 {
		title = string([]rune(title)[:256])
	}
	q.Artist = artist
	q.Title = title

	// 1. Быстрая проверка в локальном кэше (< 0.1 мс)
	if cached, hit := e.cache.Get(artist, title); hit && cached != nil {
		return cached, nil
	}

	// 2. Дедупликация конкурентных запросов на один и тот же трек
	cacheKey := MakeKey(artist, title)
	result, err := e.flight.Do(cacheKey, func() (*Lyrics, error) {
		// Double check кэша внутри singleflight
		if cached, hit := e.cache.Get(artist, title); hit && cached != nil {
			return cached, nil
		}

		return e.queryProviders(ctx, q)
	})

	if err != nil {
		return nil, err
	}

	// 3. Сохранение в кэш и асинхронная запись на диск (если включено)
	e.cache.Set(artist, title, result)
	e.writer.SaveAsync(q, result)

	return result, nil
}

func (e *Engine) queryProviders(ctx context.Context, q SearchQuery) (*Lyrics, error) {
	// Ограничиваем количество одновременных исходящих сетевых запросов
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	var plainFallback *Lyrics

	for _, p := range e.providers {
		// Создаем контекст с таймаутом на каждого провайдера
		reqCtx, cancel := context.WithTimeout(ctx, e.cfg.RequestTimeout)
		res, err := p.Fetch(reqCtx, q)
		cancel()

		if err != nil || res == nil {
			if err != nil && err != ErrNotFound {
				log.Printf("[DEBUG] [LYRICS] Ошибка провайдера %s: %v", p.Name(), err)
			}
			continue
		}

		// Если провайдер вернул инструментальный трек или синхронизированный LRC — он побеждает сразу!
		if res.Instrumental || res.Kind == KindLRC {
			log.Printf("[INFO] [LYRICS] Найден синхронизированный текст для %q - %q в %s", q.Artist, q.Title, p.Name())
			return res, nil
		}

		// Если получили plain текст, сохраняем как запасной и продолжаем опрос в поисках синхронизированного LRC
		if res.Kind == KindPlain && plainFallback == nil {
			plainFallback = res
		}
	}

	if plainFallback != nil {
		log.Printf("[INFO] [LYRICS] Найден plain текст для %q - %q в %s", q.Artist, q.Title, plainFallback.Source)
		return plainFallback, nil
	}

	return nil, ErrNotFound
}

func sanitizeInput(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r < 32 && r != '\t' {
			continue
		}
		sb.WriteRune(r)
	}
	return strings.TrimSpace(sb.String())
}

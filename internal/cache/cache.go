// Package cache предоставляет высокопроизводительное кэширование в оперативной памяти
// с поддержкой времени жизни (TTL), ограничением размера и потокобезопасным доступом.
//
// Данный файл cache.go реализует кэш поисковых ответов Navidrome (Subsonic и Native UI),
// позволяющий отдавать результаты за доли миллисекунды (<0.1ms) без повторных обращений к SQLite.
package cache

import (
	"net/http"
	"sync"
	"time"
)

// ResponseItem хранит сериализованный ответ Navidrome и метаданные HTTP.
type ResponseItem struct {
	Body        []byte
	Headers     http.Header
	StatusCode  int
	ContentType string
	ExpiresAt   time.Time
}

// MemoryCache потокобезопасный кэш с автоматической очисткой устаревших записей.
type MemoryCache struct {
	mu       sync.RWMutex
	items    map[string]ResponseItem
	maxItems int
	defaultTTL time.Duration
}

// NewMemoryCache создает новый кэш с заданным лимитом элементов и временем жизни по умолчанию.
func NewMemoryCache(maxItems int, defaultTTL time.Duration) *MemoryCache {
	if maxItems <= 0 {
		maxItems = 1000
	}
	if defaultTTL <= 0 {
		defaultTTL = 30 * time.Second
	}

	c := &MemoryCache{
		items:      make(map[string]ResponseItem, maxItems),
		maxItems:   maxItems,
		defaultTTL: defaultTTL,
	}

	// Фоновый сборщик устаревших записей раз в 30 секунд
	go c.startJanitor(30 * time.Second)

	return c
}

// Get извлекает элемент из кэша. Возвращает копию данных, если запись существует и не устарела.
func (c *MemoryCache) Get(key string) ([]byte, http.Header, int, bool) {
	c.mu.RLock()
	item, found := c.items[key]
	c.mu.RUnlock()

	if !found {
		return nil, nil, 0, false
	}

	if time.Now().After(item.ExpiresAt) {
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return nil, nil, 0, false
	}

	// Клонируем заголовки для безопасной модификации в вызывающем коде
	clonedHeaders := make(http.Header, len(item.Headers))
	for k, vv := range item.Headers {
		clonedHeaders[k] = append([]string(nil), vv...)
	}

	return item.Body, clonedHeaders, item.StatusCode, true
}

// Set сохраняет элемент в кэше с указанным TTL (если ttl <= 0, используется defaultTTL).
func (c *MemoryCache) Set(key string, body []byte, headers http.Header, statusCode int, ttl time.Duration) {
	if ttl <= 0 {
		ttl = c.defaultTTL
	}

	clonedHeaders := make(http.Header, len(headers))
	for k, vv := range headers {
		clonedHeaders[k] = append([]string(nil), vv...)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Если кэш переполнен, удаляем случайную треть устаревших или старых записей
	if len(c.items) >= c.maxItems {
		c.evictOldestLocked(c.maxItems / 3)
	}

	c.items[key] = ResponseItem{
		Body:       body,
		Headers:    clonedHeaders,
		StatusCode: statusCode,
		ExpiresAt:  time.Now().Add(ttl),
	}
}

// Clear полностью очищает кэш (например, при обновлении библиотеки или алиасов).
func (c *MemoryCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]ResponseItem, c.maxItems)
}

// evictOldestLocked вытесняет указанное число записей при превышении лимита.
func (c *MemoryCache) evictOldestLocked(count int) {
	now := time.Now()
	evicted := 0

	// Сначала чистим уже просроченные записи
	for k, item := range c.items {
		if now.After(item.ExpiresAt) {
			delete(c.items, k)
			evicted++
			if evicted >= count {
				return
			}
		}
	}

	// Если все еще нужно освободить место, удаляем любые первые записи
	for k := range c.items {
		delete(c.items, k)
		evicted++
		if evicted >= count {
			return
		}
	}
}

// startJanitor периодически очищает просроченные элементы в фоне.
func (c *MemoryCache) startJanitor(interval time.Duration) {
	ticker := time.NewTicker(interval)
	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for k, item := range c.items {
			if now.After(item.ExpiresAt) {
				delete(c.items, k)
			}
		}
		c.mu.Unlock()
	}
}

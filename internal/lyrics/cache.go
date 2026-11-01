// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл cache.go реализует двухуровневый кэш (In-Memory + SQLite) для мгновенной отдачи
// ранее найденных текстов песен и сохранения состояния между перезапусками сервиса.
package lyrics

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// CurrentCacheSchemaVersion определяет текущую версию схемы кэша текстов.
// При обновлении версии все устаревшие или невалидные записи автоматически вычищаются из SQLite.
const CurrentCacheSchemaVersion = 5

// CacheEntry представляет запись в оперативной памяти.
type CacheEntry struct {
	Lyrics    *Lyrics
	ExpiresAt time.Time
}

// Cache управляет оперативной и персистентной памятью для текстов песен.
type Cache struct {
	mu         sync.RWMutex
	mem        map[string]*CacheEntry
	db         *sql.DB
	ttl        time.Duration
	maxEntries int
	stopChan   chan struct{}
}

// NewCache создает новый экземпляр двухуровневого кэша.
func NewCache(db *sql.DB, ttl time.Duration, maxEntries int) *Cache {
	if maxEntries <= 0 {
		maxEntries = 5000
	}

	c := &Cache{
		mem:        make(map[string]*CacheEntry),
		db:         db,
		ttl:        ttl,
		maxEntries: maxEntries,
		stopChan:   make(chan struct{}),
	}

	if db != nil {
		if err := c.initDBSchema(); err != nil {
			log.Printf("[WARN] [LYRICS] Не удалось инициализировать таблицу кэша в SQLite: %v", err)
		}
	}

	// Фоновая очистка протухших записей из памяти каждые 10 минут
	go c.startJanitor(10 * time.Minute)

	return c
}

// MakeKey формирует канонический ключ кэширования для трека.
func MakeKey(artist, title string) string {
	a := strings.ToLower(strings.TrimSpace(artist))
	t := strings.ToLower(strings.TrimSpace(title))
	return fmt.Sprintf("%s ::: %s", a, t)
}

// Get возвращает текст песни из памяти или из SQLite, если он не протух.
func (c *Cache) Get(artist, title string) (*Lyrics, bool) {
	key := MakeKey(artist, title)
	now := time.Now()

	// 1. Проверка в оперативной памяти (In-Memory)
	c.mu.RLock()
	entry, found := c.mem[key]
	c.mu.RUnlock()

	if found {
		if now.Before(entry.ExpiresAt) {
			if c.isLyricsValidForTrack(artist, title, entry.Lyrics) {
				return entry.Lyrics, true
			}
			// Запись в памяти не прошла валидацию качества — удаляем
			_ = c.Delete(artist, title)
		} else {
			c.mu.Lock()
			delete(c.mem, key)
			c.mu.Unlock()
		}
	}

	// 2. Проверка в SQLite персистентном кэше
	if c.db == nil {
		return nil, false
	}

	lyr, err := c.loadFromDB(key, now)
	if err == nil && lyr != nil {
		if !c.isLyricsValidForTrack(artist, title, lyr) {
			_ = c.Delete(artist, title)
			return nil, false
		}

		// Подтягиваем в оперативную память для будущих быстрых хитов
		c.mu.Lock()
		c.mem[key] = &CacheEntry{
			Lyrics:    lyr,
			ExpiresAt: now.Add(c.ttl),
		}
		c.mu.Unlock()
		return lyr, true
	}

	return nil, false
}

// Delete удаляет запись для трека из оперативной памяти и персистентного кэша SQLite.
func (c *Cache) Delete(artist, title string) error {
	key := MakeKey(artist, title)
	c.mu.Lock()
	delete(c.mem, key)
	c.mu.Unlock()

	if c.db != nil {
		_, err := c.db.Exec("DELETE FROM _poisq_lyrics_cache WHERE cache_key = ?", key)
		return err
	}
	return nil
}

// Clear полностью очищает кэш в оперативной памяти и в SQLite.
func (c *Cache) Clear() error {
	c.mu.Lock()
	c.mem = make(map[string]*CacheEntry)
	c.mu.Unlock()

	if c.db != nil {
		_, err := c.db.Exec("DELETE FROM _poisq_lyrics_cache")
		return err
	}
	return nil
}

func (c *Cache) isLyricsValidForTrack(artist, title string, lyr *Lyrics) bool {
	if lyr == nil {
		return false
	}
	if lyr.Instrumental {
		return true
	}
	text := lyr.PlainLyrics
	if text == "" && lyr.SyncedLyrics != "" {
		text = lyr.SyncedLyrics
	}
	if strings.TrimSpace(text) == "" {
		return false
	}
	if IsDefectiveLyrics(text) {
		return false
	}
	return true
}

// Set сохраняет текст песни в памяти и персистентно в SQLite.
func (c *Cache) Set(artist, title string, lyr *Lyrics) {
	if lyr == nil {
		return
	}
	key := MakeKey(artist, title)
	now := time.Now()
	expiresAt := now.Add(c.ttl)

	// 1. Сохранение в оперативной памяти с контролем максимальной емкости
	c.mu.Lock()
	if len(c.mem) >= c.maxEntries {
		// Удаляем протухшие записи для освобождения места
		for k, v := range c.mem {
			if now.After(v.ExpiresAt) {
				delete(c.mem, k)
			}
		}
		// Если память все еще заполнена — вытесняем 10% старых записей
		if len(c.mem) >= c.maxEntries {
			toRemove := c.maxEntries / 10
			if toRemove < 1 {
				toRemove = 1
			}
			for k := range c.mem {
				delete(c.mem, k)
				toRemove--
				if toRemove <= 0 {
					break
				}
			}
		}
	}
	c.mem[key] = &CacheEntry{
		Lyrics:    lyr,
		ExpiresAt: expiresAt,
	}
	c.mu.Unlock()

	// 2. Фоновое сохранение в SQLite
	if c.db != nil {
		go func() {
			if err := c.saveToDB(key, artist, title, lyr, expiresAt); err != nil {
				log.Printf("[WARN] [LYRICS] Ошибка сохранения текста в SQLite кэш: %v", err)
			}
		}()
	}
}

// Close останавливает фоновые фоновые задачи кэша.
func (c *Cache) Close() {
	close(c.stopChan)
}

func (c *Cache) initDBSchema() error {
	query := `
	CREATE TABLE IF NOT EXISTS _poisq_lyrics_cache (
		cache_key TEXT PRIMARY KEY,
		artist TEXT NOT NULL,
		title TEXT NOT NULL,
		kind TEXT NOT NULL,
		plain_lyrics TEXT,
		synced_lyrics TEXT,
		lines_json TEXT,
		source TEXT NOT NULL,
		instrumental INTEGER NOT NULL DEFAULT 0,
		expires_at INTEGER NOT NULL,
		cached_at INTEGER NOT NULL,
		schema_version INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_poisq_lyrics_expires ON _poisq_lyrics_cache(expires_at);
	`
	if _, err := c.db.Exec(query); err != nil {
		return err
	}

	// Миграция: добавляем schema_version, если таблица была создана ранее без нее
	_, _ = c.db.Exec("ALTER TABLE _poisq_lyrics_cache ADD COLUMN schema_version INTEGER NOT NULL DEFAULT 0;")

	// Автоматически очищаем невалидный кэш, созданный предыдущими версиями (v0.0.1 - v0.0.3)
	res, err := c.db.Exec("DELETE FROM _poisq_lyrics_cache WHERE schema_version < ?;", CurrentCacheSchemaVersion)
	if err == nil {
		if rows, _ := res.RowsAffected(); rows > 0 {
			log.Printf("[INFO] [LYRICS] Очищено %d устаревших записей кэша текстов (схема обновлена до v%d)", rows, CurrentCacheSchemaVersion)
		}
	}

	return nil
}

func (c *Cache) loadFromDB(key string, now time.Time) (*Lyrics, error) {
	query := `
	SELECT kind, plain_lyrics, synced_lyrics, lines_json, source, instrumental, cached_at
	FROM _poisq_lyrics_cache
	WHERE cache_key = ? AND expires_at > ? AND schema_version >= ?
	`
	var (
		kind         string
		plainLyrics  sql.NullString
		syncedLyrics sql.NullString
		linesJSON    sql.NullString
		source       string
		instrumental int
		cachedAtSec  int64
	)

	err := c.db.QueryRow(query, key, now.Unix(), CurrentCacheSchemaVersion).Scan(
		&kind, &plainLyrics, &syncedLyrics, &linesJSON, &source, &instrumental, &cachedAtSec,
	)
	if err != nil {
		return nil, err
	}

	var lines []Line
	if linesJSON.Valid && linesJSON.String != "" {
		_ = json.Unmarshal([]byte(linesJSON.String), &lines)
	}

	return &Lyrics{
		Kind:         LyricsKind(kind),
		PlainLyrics:  plainLyrics.String,
		SyncedLyrics: syncedLyrics.String,
		Lines:        lines,
		Source:       source,
		Instrumental: instrumental == 1,
		CachedAt:     time.Unix(cachedAtSec, 0),
	}, nil
}

func (c *Cache) saveToDB(key, artist, title string, lyr *Lyrics, expiresAt time.Time) error {
	var linesJSON string
	if len(lyr.Lines) > 0 {
		if bytes, err := json.Marshal(lyr.Lines); err == nil {
			linesJSON = string(bytes)
		}
	}

	instrumentalInt := 0
	if lyr.Instrumental {
		instrumentalInt = 1
	}

	query := `
	INSERT INTO _poisq_lyrics_cache (
		cache_key, artist, title, kind, plain_lyrics, synced_lyrics, lines_json, source, instrumental, expires_at, cached_at, schema_version
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(cache_key) DO UPDATE SET
		kind = excluded.kind,
		plain_lyrics = excluded.plain_lyrics,
		synced_lyrics = excluded.synced_lyrics,
		lines_json = excluded.lines_json,
		source = excluded.source,
		instrumental = excluded.instrumental,
		expires_at = excluded.expires_at,
		cached_at = excluded.cached_at,
		schema_version = excluded.schema_version;
	`

	_, err := c.db.Exec(query,
		key,
		artist,
		title,
		string(lyr.Kind),
		lyr.PlainLyrics,
		lyr.SyncedLyrics,
		linesJSON,
		lyr.Source,
		instrumentalInt,
		expiresAt.Unix(),
		lyr.CachedAt.Unix(),
		CurrentCacheSchemaVersion,
	)
	return err
}

func (c *Cache) startJanitor(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case now := <-ticker.C:
			c.cleanExpired(now)
		}
	}
}

func (c *Cache) cleanExpired(now time.Time) {
	c.mu.Lock()
	for k, v := range c.mem {
		if now.After(v.ExpiresAt) {
			delete(c.mem, k)
		}
	}
	c.mu.Unlock()

	if c.db != nil {
		_, _ = c.db.Exec("DELETE FROM _poisq_lyrics_cache WHERE expires_at <= ?", now.Unix())
	}
}

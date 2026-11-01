// Package lyrics_test тестирует двухуровневый кэш текстов песен (In-Memory + SQLite).
//
// Данный файл cache_test.go проверяет сохранение, извлечение, время жизни (TTL)
// и персистентность данных между оперативными записями и базой данных SQLite.
package lyrics_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/lyrics"
	_ "modernc.org/sqlite"
)

func TestCache_InMemory(t *testing.T) {
	cache := lyrics.NewCache(nil, 50*time.Millisecond, 1000)
	defer cache.Close()

	lyr := &lyrics.Lyrics{
		Kind:         lyrics.KindLRC,
		SyncedLyrics: "[00:10.00] Привет",
		PlainLyrics:  "Привет",
		Source:       "test",
		CachedAt:     time.Now(),
	}

	cache.Set("Кино", "Группа крови", lyr)

	// Должен мгновенно находиться в кэше
	found, ok := cache.Get("Кино", "Группа крови")
	if !ok || found == nil {
		t.Fatalf("Ожидался хит кэша")
	}
	if found.SyncedLyrics != "[00:10.00] Привет" {
		t.Errorf("Неверное содержимое: %s", found.SyncedLyrics)
	}

	// Поиск нечувствителен к регистру и пробелам
	foundCase, okCase := cache.Get("  кино  ", "группа КРОВИ ")
	if !okCase || foundCase == nil {
		t.Errorf("Ожидался хит кэша при разном регистре")
	}

	// Ждем протухания TTL
	time.Sleep(70 * time.Millisecond)
	_, okExpired := cache.Get("Кино", "Группа крови")
	if okExpired {
		t.Errorf("Запись не должна быть найдена после истечения TTL")
	}
}

func TestCache_SQLitePersistence(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Ошибка открытия in-memory SQLite: %v", err)
	}
	defer db.Close()

	cache := lyrics.NewCache(db, 1*time.Hour, 1000)
	defer cache.Close()

	lyr := &lyrics.Lyrics{
		Kind:         lyrics.KindLRC,
		SyncedLyrics: "[00:05.00] Строка в базе",
		PlainLyrics:  "Строка в базе",
		Lines: []lyrics.Line{
			{Text: "Строка в базе", StartMs: 5000, DurationMs: 3000},
		},
		Source:   "yandex",
		CachedAt: time.Now(),
	}

	cache.Set("Ария", "Штиль", lyr)

	// Даем фоновой горутине завершить запись в SQLite
	time.Sleep(50 * time.Millisecond)

	// Создаем новый кэш над той же БД (имитация перезапуска сервиса с пустой оперативной памятью)
	newCache := lyrics.NewCache(db, 1*time.Hour, 1000)
	defer newCache.Close()

	loaded, ok := newCache.Get("Ария", "Штиль")
	if !ok || loaded == nil {
		t.Fatalf("Ожидалось чтение записи из SQLite после перезапуска кэша")
	}

	if loaded.Source != "yandex" || loaded.SyncedLyrics != "[00:05.00] Строка в базе" {
		t.Errorf("Некорректно восстановленные данные: %+v", loaded)
	}
	if len(loaded.Lines) != 1 || loaded.Lines[0].StartMs != 5000 {
		t.Errorf("Некорректно восстановленные Lines: %+v", loaded.Lines)
	}
}

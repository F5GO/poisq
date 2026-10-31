// Package cache предоставляет высокопроизводительное кэширование в оперативной памяти
// с поддержкой времени жизни (TTL), ограничением размера и потокобезопасным доступом.
//
// Данный файл cache_test.go содержит юнит-тесты для MemoryCache и SingleFlight:
// проверку попадания в кэш (hit/miss), истечения срока жизни (TTL), вытеснения при переполнении
// и дедупликации параллельных запросов.
package cache

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryCache_Basic(t *testing.T) {
	c := NewMemoryCache(10, 50*time.Millisecond)

	// Miss
	if _, _, _, ok := c.Get("key1"); ok {
		t.Fatal("Ожидался cache miss для key1")
	}

	headers := http.Header{"Content-Type": []string{"application/json"}}
	c.Set("key1", []byte("hello world"), headers, http.StatusOK, 50*time.Millisecond)

	// Hit
	body, h, code, ok := c.Get("key1")
	if !ok || string(body) != "hello world" || code != http.StatusOK {
		t.Fatalf("Ожидался cache hit, получено: ok=%v, body=%s, code=%d", ok, body, code)
	}
	if h.Get("Content-Type") != "application/json" {
		t.Fatalf("Заголовок Content-Type не совпадает: %s", h.Get("Content-Type"))
	}

	// Expiration
	time.Sleep(60 * time.Millisecond)
	if _, _, _, ok := c.Get("key1"); ok {
		t.Fatal("Запись должна была истечь по TTL")
	}
}

func TestMemoryCache_Eviction(t *testing.T) {
	c := NewMemoryCache(3, 1*time.Minute)

	c.Set("k1", []byte("1"), nil, 200, 0)
	c.Set("k2", []byte("2"), nil, 200, 0)
	c.Set("k3", []byte("3"), nil, 200, 0)
	// Превышение лимита в 3 элемента
	c.Set("k4", []byte("4"), nil, 200, 0)

	c.mu.RLock()
	count := len(c.items)
	c.mu.RUnlock()

	if count > 3 {
		t.Fatalf("Количество элементов (%d) превышает лимит maxItems (3)", count)
	}
}

func TestSingleFlight_Deduplication(t *testing.T) {
	sf := NewSingleFlight()
	var callCount int32

	var wg sync.WaitGroup
	results := make([]string, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			body, _, _, err := sf.Do("same_key", func() ([]byte, http.Header, int, error) {
				atomic.AddInt32(&callCount, 1)
				time.Sleep(20 * time.Millisecond)
				return []byte("expensive_result"), nil, http.StatusOK, nil
			})
			if err != nil {
				t.Errorf("Ошибка: %v", err)
			}
			results[idx] = string(body)
		}(i)
	}

	wg.Wait()

	if atomic.LoadInt32(&callCount) != 1 {
		t.Fatalf("Функция должна быть вызвана ровно 1 раз, вызвано: %d", callCount)
	}

	for _, res := range results {
		if res != "expensive_result" {
			t.Fatalf("Некорректный результат: %s", res)
		}
	}
}

func TestSingleFlight_ErrorHandling(t *testing.T) {
	sf := NewSingleFlight()
	expectedErr := errors.New("upstream failed")

	_, _, _, err := sf.Do("err_key", func() ([]byte, http.Header, int, error) {
		return nil, nil, 502, expectedErr
	})

	if err != expectedErr {
		t.Fatalf("Ожидалась ошибка upstream failed, получено: %v", err)
	}
}

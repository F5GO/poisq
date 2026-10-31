// Package cache предоставляет высокопроизводительное кэширование в оперативной памяти
// с поддержкой времени жизни (TTL), ограничением размера и потокобезопасным доступом.
//
// Данный файл singleflight.go реализует подавление дублирующих параллельных запросов к Navidrome
// (например, когда клиент Amperfy или браузер отправляет несколько одинаковых запросов поиска одновременно).
package cache

import (
	"net/http"
	"sync"
)

// call представляет выполняемый или завершенный сетевой запрос.
type call struct {
	wg     sync.WaitGroup
	body   []byte
	header http.Header
	code   int
	err    error
}

// SingleFlight координирует одновременные одинаковые запросы, выполняя только один из них.
type SingleFlight struct {
	mu sync.Mutex
	m  map[string]*call
}

// NewSingleFlight создает новый экземпляр SingleFlight.
func NewSingleFlight() *SingleFlight {
	return &SingleFlight{
		m: make(map[string]*call),
	}
}

// Do выполняет переданную функцию fn только один раз для заданного ключа, если поступают одновременные запросы.
// Все параллельные вызовы с тем же ключом ожидают завершения первого вызова и получают одинаковый результат.
func (g *SingleFlight) Do(key string, fn func() ([]byte, http.Header, int, error)) ([]byte, http.Header, int, error) {
	g.mu.Lock()
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.body, c.header, c.code, c.err
	}

	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.body, c.header, c.code, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.body, c.header, c.code, c.err
}

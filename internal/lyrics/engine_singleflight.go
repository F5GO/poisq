// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл engine_singleflight.go реализует подавление дублирующих параллельных запросов
// поиска слов песни к внешним провайдерам, гарантируя отправку ровно одного сетевого запроса.
package lyrics

import "sync"

type lyricsCall struct {
	wg  sync.WaitGroup
	lyr *Lyrics
	err error
}

// SingleFlightGroup координирует конкурентные поиски текстов для одинаковых треков.
type SingleFlightGroup struct {
	mu sync.Mutex
	m  map[string]*lyricsCall
}

// NewSingleFlightGroup создает новую группу дедупликации.
func NewSingleFlightGroup() *SingleFlightGroup {
	return &SingleFlightGroup{
		m: make(map[string]*lyricsCall),
	}
}

// Do выполняет функцию fn один раз для заданного ключа, возвращая одинаковый результат всем ожидающим горутинам.
func (g *SingleFlightGroup) Do(key string, fn func() (*Lyrics, error)) (*Lyrics, error) {
	g.mu.Lock()
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.lyr, c.err
	}

	c := new(lyricsCall)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.lyr, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.lyr, c.err
}

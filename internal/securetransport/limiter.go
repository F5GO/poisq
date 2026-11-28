package securetransport

import (
	"sync"
	"time"
)

// rateLimiter — token bucket на ключ (адрес клиента, имя пользователя) с ограниченным числом ключей.
type rateLimiter struct {
	perMinute  float64
	burst      float64
	maxEntries int

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMinute, burst float64, maxEntries int) *rateLimiter {
	return &rateLimiter{perMinute: perMinute, burst: burst, maxEntries: maxEntries, buckets: make(map[string]*bucket)}
}

func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= l.maxEntries {
			refill := time.Duration(l.burst / l.perMinute * float64(time.Minute))
			for k, old := range l.buckets {
				if now.Sub(old.last) >= refill {
					delete(l.buckets, k)
				}
			}
			// Таблица полна действующими ключами: новых не пускаем, а не вытесняем старых.
			if len(l.buckets) >= l.maxEntries {
				return false
			}
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Minutes()*l.perMinute)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

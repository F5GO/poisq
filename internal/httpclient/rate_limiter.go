// Package httpclient предоставляет отказоустойчивый HTTP-клиент со встроенным
// механизмом Circuit Breaker, Smart Retry (экспоненциальный бэкап + Jitter) и
// индивидуальным Rate Limiter для защиты от блокировок по IP.
//
// Данный файл rate_limiter.go реализует Fan-Out Rate Limiter для ограничения
// параллельных запросов и минимального интервала между вызовами к каждому провайдеру.
package httpclient

import (
	"context"
	"sync"
	"time"
)

// ProviderLimiter ограничивает частоту и параллелизм запросов к одному внешнему API.
type ProviderLimiter struct {
	sem         chan struct{}
	mu          sync.Mutex
	minInterval time.Duration
	lastCall    time.Time
}

// NewProviderLimiter создает ограничитель с максимальным числом параллельных запросов и минимальной паузой между ними.
func NewProviderLimiter(maxConcurrent int, minInterval time.Duration) *ProviderLimiter {
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	return &ProviderLimiter{
		sem:         make(chan struct{}, maxConcurrent),
		minInterval: minInterval,
	}
}

// Wait захватывает слот параллелизма и выдерживает минимальный интервал между запросами.
// Возвращает функцию освобождения слота, которую необходимо вызвать через defer.
func (l *ProviderLimiter) Wait(ctx context.Context) (func(), error) {
	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if l.minInterval > 0 {
		l.mu.Lock()
		now := time.Now()
		nextAllowed := l.lastCall.Add(l.minInterval)
		var waitDur time.Duration
		if now.Before(nextAllowed) {
			waitDur = nextAllowed.Sub(now)
			l.lastCall = nextAllowed
		} else {
			l.lastCall = now
		}
		l.mu.Unlock()

		if waitDur > 0 {
			timer := time.NewTimer(waitDur)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				<-l.sem
				return nil, ctx.Err()
			}
		}
	}

	return func() { <-l.sem }, nil
}

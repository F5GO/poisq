// Package httpclient предоставляет отказоустойчивый HTTP-клиент со встроенным
// механизмом Circuit Breaker, Smart Retry (экспоненциальный бэкап + Jitter) и
// индивидуальным Rate Limiter для защиты от блокировок по IP.
//
// Данный файл circuit_breaker.go реализует паттерн Circuit Breaker: если внешний
// источник падает несколько раз подряд, он временно исключается из опроса на 3 минуты,
// не задерживая обработку очереди.
package httpclient

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen возвращается, когда провайдер временно отключен из-за серии сбоев.
var ErrCircuitOpen = errors.New("circuit breaker: источник временно отключен на 3 минуты из-за сбоев")

// CircuitBreaker отслеживает здоровье конкретного внешнего провайдера.
type CircuitBreaker struct {
	mu               sync.Mutex
	failureThreshold int
	openDuration     time.Duration
	failures         int
	openUntil        time.Time
	halfOpenInFlight bool
}

// NewCircuitBreaker создает новый автоматический выключатель (по умолчанию: 3 сбоя -> пауза 3 минуты).
func NewCircuitBreaker(failureThreshold int, openDuration time.Duration) *CircuitBreaker {
	if failureThreshold <= 0 {
		failureThreshold = 3
	}
	if openDuration <= 0 {
		openDuration = 3 * time.Minute
	}
	return &CircuitBreaker{
		failureThreshold: failureThreshold,
		openDuration:     openDuration,
	}
}

// Allow проверяет, разрешено ли отправлять запрос к источнику в данный момент.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.openUntil.IsZero() {
		return !cb.halfOpenInFlight
	}
	if time.Now().After(cb.openUntil) {
		// Переход в Half-Open: пропускаем строго один пробный запрос
		if cb.halfOpenInFlight {
			return false
		}
		cb.openUntil = time.Time{}
		cb.failures = cb.failureThreshold - 1
		cb.halfOpenInFlight = true
		return true
	}
	return false
}

// RecordSuccess сбрасывает счетчик ошибок после успешного ответа.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	cb.openUntil = time.Time{}
	cb.halfOpenInFlight = false
}

// RecordFailure регистрирует сетевой сбой или ошибку 5xx и при достижении порога размыкает цепь на 3 минуты.
// Возвращает true, если именно этот сбой разомкнул цепь (для явного лога в оркестраторе).
func (cb *CircuitBreaker) RecordFailure() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.halfOpenInFlight = false
	cb.failures++
	if cb.failures >= cb.failureThreshold {
		cb.openUntil = time.Now().Add(cb.openDuration)
		return true
	}
	return false
}

// ReleaseHalfOpen освобождает слот пробного запроса без учёта ошибки (например, при отмене или локальной политике).
func (cb *CircuitBreaker) ReleaseHalfOpen() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.halfOpenInFlight {
		cb.halfOpenInFlight = false
		cb.openUntil = time.Now().Add(cb.openDuration)
	}
}

// OpenFor возвращает, сколько времени источник еще будет отключен (0 — цепь замкнута).
func (cb *CircuitBreaker) OpenFor() time.Duration {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.openUntil.IsZero() {
		return 0
	}
	if d := time.Until(cb.openUntil); d > 0 {
		return d
	}
	return 0
}

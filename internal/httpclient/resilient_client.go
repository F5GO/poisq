// Package httpclient предоставляет отказоустойчивый HTTP-клиент со встроенным
// механизмом Circuit Breaker, Smart Retry (экспоненциальный бэкап + Jitter) и
// индивидуальным Rate Limiter для защиты от блокировок по IP.
//
// Данный файл resilient_client.go реализует выполнение HTTP-запросов с экспоненциальным
// бэкапом и случайным Jitter (до 5 попыток), автоматическим контролем размера ответа
// через io.LimitReader и интеграцией с Circuit Breaker.
package httpclient

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"
)

// ResilientClient объединяет HTTP-клиент, Rate Limiter и Circuit Breaker для безопасных сетевых вызовов.
type ResilientClient struct {
	client     *http.Client
	limiter    *ProviderLimiter
	breaker    *CircuitBreaker
	maxRetries int
	maxBytes   int64
}

// NewResilientClient создает защищенный HTTP-клиент для внешнего провайдера.
func NewResilientClient(timeout time.Duration, maxConcurrent int, minInterval time.Duration, maxRetries int) *ResilientClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if maxRetries <= 0 {
		maxRetries = 3
	}
	return &ResilientClient{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		limiter:    NewProviderLimiter(maxConcurrent, minInterval),
		breaker:    NewCircuitBreaker(3, 3*time.Minute),
		maxRetries: maxRetries,
		maxBytes:   4 * 1024 * 1024, // 4 МБ максимум для текстовых/JSON ответов
	}
}

// DoWithRetry выполняет запрос с проверкой Circuit Breaker, Rate Limiter и Smart Retry (Exponential Backoff + Jitter).
func (rc *ResilientClient) DoWithRetry(ctx context.Context, buildReq func() (*http.Request, error)) ([]byte, int, http.Header, error) {
	if !rc.breaker.Allow() {
		return nil, 0, nil, ErrCircuitOpen
	}

	release, err := rc.limiter.Wait(ctx)
	if err != nil {
		return nil, 0, nil, err
	}
	defer release()

	var lastErr error
	var lastStatus int

	for attempt := 1; attempt <= rc.maxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, 0, nil, ctx.Err()
		}

		req, err := buildReq()
		if err != nil {
			return nil, 0, nil, err
		}

		resp, err := rc.client.Do(req.WithContext(ctx))
		if err != nil {
			lastErr = err
			if attempt == rc.maxRetries {
				rc.breaker.RecordFailure()
				break
			}
			sleepWithJitter(ctx, attempt)
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, rc.maxBytes))
		_ = resp.Body.Close()
		lastStatus = resp.StatusCode

		if readErr != nil {
			lastErr = readErr
			if attempt == rc.maxRetries {
				rc.breaker.RecordFailure()
				break
			}
			sleepWithJitter(ctx, attempt)
			continue
		}

		// Серверные сбои 5xx или временные 429 — кандидаты на ретрай
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
			if attempt == rc.maxRetries {
				rc.breaker.RecordFailure()
				break
			}
			sleepWithJitter(ctx, attempt)
			continue
		}

		rc.breaker.RecordSuccess()
		return body, resp.StatusCode, resp.Header, nil
	}

	return nil, lastStatus, nil, lastErr
}

func sleepWithJitter(ctx context.Context, attempt int) {
	base := time.Duration(150*(1<<(attempt-1))) * time.Millisecond
	if base > 3*time.Second {
		base = 3 * time.Second
	}
	jitter := time.Duration(rand.Intn(120)) * time.Millisecond
	timer := time.NewTimer(base + jitter)
	select {
	case <-timer.C:
	case <-ctx.Done():
		timer.Stop()
	}
}

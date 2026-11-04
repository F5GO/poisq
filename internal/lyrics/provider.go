// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл provider.go определяет базовый интерфейс Provider, контракт взаимодействия
// и общие ошибки получения текстов.
package lyrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

var (
	// ErrNotFound возвращается, если текст песни не найден в данном источнике.
	ErrNotFound = errors.New("lyrics not found")

	// ErrRateLimited возвращается при превышении лимитов запросов к источнику.
	ErrRateLimited = errors.New("rate limited by provider")
)

// Provider описывает интерфейс любого источника текстов песен.
type Provider interface {
	// ID возвращает краткий машиночитаемый идентификатор источника (например, "yandex", "lrclib").
	ID() string

	// Name возвращает человекочитаемое наименование источника.
	Name() string

	// Fetch выполняет поиск и получение текста песни по переданным метаданным.
	Fetch(ctx context.Context, query SearchQuery) (*Lyrics, error)
}

// httpStatusError превращает неуспешный HTTP-статус провайдера в ошибку с кодом.
// 404 остается обычным ErrNotFound («трека нет»), а 401/403/429/5xx становятся
// явными сбоями, которые видны в логах и учитываются Circuit Breaker.
func httpStatusError(provider string, status int) error {
	if status == http.StatusNotFound {
		return ErrNotFound
	}
	return fmt.Errorf("%s: HTTP %d", provider, status)
}

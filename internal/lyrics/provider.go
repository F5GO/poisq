// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл provider.go определяет базовый интерфейс Provider, контракт взаимодействия
// и общие ошибки получения текстов.
package lyrics

import (
	"context"
	"errors"
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

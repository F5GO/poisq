// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл provider_lrclib.go конфигурирует подключение к официальному глобальному сервису LRCLIB.
package lyrics

import (
	"time"
)

const defaultLrclibURL = "https://lrclib.net"

// NewLrclibProvider создает экземпляр официального LRCLIB провайдера.
func NewLrclibProvider(timeout, tolerance time.Duration) *CustomLrclibProvider {
	return NewCustomLrclibProvider("lrclib", "LRCLIB", defaultLrclibURL, timeout, tolerance)
}

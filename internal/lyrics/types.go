// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл types.go определяет базовые доменные типы, форматы текстов, структуры синхронизированных строк
// и параметры поисковых запросов к провайдерам текстов.
package lyrics

import "time"

// LyricsKind определяет тип текста песни (синхронизированный LRC, построчный, обычный plain или инструментальный).
type LyricsKind string

const (
	// KindLRC — построчно синхронизированный текст с таймингами вида [mm:ss.xx].
	KindLRC LyricsKind = "lrc"

	// KindPlain — обычный несинхронизированный текст песни.
	KindPlain LyricsKind = "plain"

	// KindInstrumental — отметка о том, что трек является инструментальным (без слов).
	KindInstrumental LyricsKind = "instrumental"
)

// Line представляет отдельную строку текста с временными метками для синхронизированного воспроизведения.
type Line struct {
	// Text — текстовое содержимое строки.
	Text string `json:"text"`

	// StartMs — время начала строки в миллисекундах.
	StartMs int64 `json:"startMs,omitempty"`

	// DurationMs — длительность отображения строки в миллисекундах.
	DurationMs int64 `json:"durationMs,omitempty"`
}

// SearchQuery содержит атрибуты трека для поиска текста в различных источниках.
type SearchQuery struct {
	// Artist — имя основного исполнителя.
	Artist string

	// Title — название композиции.
	Title string

	// Album — название альбома (опционально, для повышения точности).
	Album string

	// Duration — продолжительность трека в секундах (для сверки с базой данных провайдера).
	Duration time.Duration

	// TrackID — уникальный идентификатор трека в Navidrome/Subsonic (для точного связывания кэша).
	TrackID string
}

// Lyrics представляет результат поиска слов песни от одного из провайдеров.
type Lyrics struct {
	// Kind — формат текста (LRC, Plain или Instrumental).
	Kind LyricsKind `json:"kind"`

	// PlainLyrics — полный текст песни без таймингов.
	PlainLyrics string `json:"plainLyrics,omitempty"`

	// SyncedLyrics — синхронизированный текст в формате стандартного LRC ([00:12.34] текст).
	SyncedLyrics string `json:"syncedLyrics,omitempty"`

	// Lines — разобранные структурированные строки (для OpenSubsonic structuredLyrics).
	Lines []Line `json:"lines,omitempty"`

	// Source — название источника, предоставившего текст (например, "yandex", "genius", "lrclib").
	Source string `json:"source"`

	// Instrumental — флаг инструментального трека.
	Instrumental bool `json:"instrumental"`

	// CachedAt — временная метка сохранения в кэш.
	CachedAt time.Time `json:"cachedAt"`
}

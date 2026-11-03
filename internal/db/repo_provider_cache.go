// Package db отвечает за безопасное взаимодействие с базами данных SQLite (navidrome.db и poisq.db).
//
// Данный файл repo_provider_cache.go реализует хранение сырых ответов всех 5 провайдеров
// текстов песен (LRCLIB, Яндекс Музыка, Musixmatch, NetEase, Genius) и результатов AI-обработки,
// позволяя пользователю мгновенно переключаться между ними в веб-интерфейсе без повторных сетевых запросов.
package db

import (
	"database/sql"
	"time"
)

// ProviderCacheRecord представляет закэшированный ответ одного источника текстов для конкретного трека.
type ProviderCacheRecord struct {
	ID           int64   `json:"id"`
	TrackID      string  `json:"trackId"`
	Provider     string  `json:"provider"`     // "lrclib", "yandex", "musixmatch", "netease", "genius", "gemini_consensus", "whisper_karaoke"
	Kind         string  `json:"kind"`         // "lrc", "richsync", "plain", "instrumental"
	Score        float64 `json:"score"`        // Оценка качества CandidateScore (0..100)
	RawLyrics    string  `json:"rawLyrics"`    // Текст или LRC-разметка
	MetadataJSON string  `json:"metadataJson"` // Доп. метаданные найденного кандидата
	FetchedAt    int64   `json:"fetchedAt"`
}

// ProviderCacheRepository инкапсулирует работу с таблицей provider_cache.
type ProviderCacheRepository struct {
	db *sql.DB
}

// NewProviderCacheRepository создает экземпляр репозитория кэша провайдеров.
func NewProviderCacheRepository(db *sql.DB) *ProviderCacheRepository {
	return &ProviderCacheRepository{db: db}
}

// Upsert сохраняет или обновляет ответ конкретного провайдера для трека.
func (r *ProviderCacheRepository) Upsert(rec ProviderCacheRecord) error {
	now := time.Now().Unix()
	if rec.MetadataJSON == "" {
		rec.MetadataJSON = "{}"
	}
	_, err := r.db.Exec(`
		INSERT INTO provider_cache (track_id, provider, kind, score, raw_lyrics, metadata_json, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(track_id, provider) DO UPDATE SET
			kind = excluded.kind,
			score = excluded.score,
			raw_lyrics = excluded.raw_lyrics,
			metadata_json = excluded.metadata_json,
			fetched_at = excluded.fetched_at
	`, rec.TrackID, rec.Provider, rec.Kind, rec.Score, rec.RawLyrics, rec.MetadataJSON, now)
	return err
}

// Delete удаляет ответ конкретного провайдера из кэша для трека (например, сброс промежуточного whisper_raw при перезапуске).
func (r *ProviderCacheRepository) Delete(trackID, provider string) error {
	_, err := r.db.Exec(`DELETE FROM provider_cache WHERE track_id = ? AND provider = ?`, trackID, provider)
	return err
}

// ListByTrack возвращает все сохраненные варианты текста от разных провайдеров для указанного трека,
// отсортированные по убыванию оценки качества (score).
func (r *ProviderCacheRepository) ListByTrack(trackID string) ([]ProviderCacheRecord, error) {
	rows, err := r.db.Query(`
		SELECT id, track_id, provider, kind, score, raw_lyrics, metadata_json, fetched_at
		FROM provider_cache
		WHERE track_id = ?
		ORDER BY score DESC, fetched_at DESC
	`, trackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ProviderCacheRecord
	for rows.Next() {
		var p ProviderCacheRecord
		if err := rows.Scan(
			&p.ID, &p.TrackID, &p.Provider, &p.Kind, &p.Score,
			&p.RawLyrics, &p.MetadataJSON, &p.FetchedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

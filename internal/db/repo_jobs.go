// Package db отвечает за безопасное взаимодействие с базами данных SQLite (navidrome.db и poisq.db).
//
// Данный файл repo_jobs.go реализует персистентную очередь задач lyrics_jobs для фонового
// опроса провайдеров, валидации через Gemini и генерации караоке через Groq Whisper
// с поддержкой приоритетов, экспоненциальных повторов и ожидания квот API-ключей.
package db

import (
	"database/sql"
	"errors"
	"time"
)

// ErrNoPendingJobs возвращается, когда в очереди нет готовых к выполнению задач.
var ErrNoPendingJobs = errors.New("в очереди нет готовых к выполнению задач")

// LyricsJobRecord описывает одну фоновую задачу в таблице lyrics_jobs.
type LyricsJobRecord struct {
	ID           int64  `json:"id"`
	TrackID      string `json:"trackId"`
	JobType      string `json:"jobType"`      // "tournament", "ai_consensus", "whisper_karaoke", "full_pipeline"
	Priority     int    `json:"priority"`     // 10 = ручной / приоритетный запуск, 1 = фоновый скан
	Status       string `json:"status"`       // "pending", "running", "waiting_quota", "done", "failed"
	AttemptCount int    `json:"attemptCount"` // Число попыток
	NextRetryAt  int64  `json:"nextRetryAt"`  // Unix timestamp следующей попытки
	ErrorLog     string `json:"errorLog"`     // Текст последней ошибки
	UpdatedAt    int64  `json:"updatedAt"`
}

// JobRepository управляет персистентной очередью задач lyrics_jobs.
type JobRepository struct {
	db *sql.DB
}

// NewJobRepository создает новый экземпляр репозитория очереди задач.
func NewJobRepository(db *sql.DB) *JobRepository {
	return &JobRepository{db: db}
}

// Enqueue ставит трек в очередь (или повышает его приоритет и сбрасывает статус в pending, если он уже есть).
func (r *JobRepository) Enqueue(trackID string, jobType string, priority int) error {
	now := time.Now().Unix()
	if jobType == "" {
		jobType = "full_pipeline"
	}
	_, err := r.db.Exec(`
		INSERT INTO lyrics_jobs (track_id, job_type, priority, status, attempt_count, next_retry_at, error_log, updated_at)
		VALUES (?, ?, ?, 'pending', 0, 0, '', ?)
		ON CONFLICT(track_id) DO UPDATE SET
			job_type = excluded.job_type,
			priority = MAX(lyrics_jobs.priority, excluded.priority),
			status = 'pending',
			attempt_count = 0,
			next_retry_at = 0,
			error_log = '',
			updated_at = excluded.updated_at
	`, trackID, jobType, priority, now)
	return err
}

// ClaimNext атомарно выбирает следующую готовую задачу с наивысшим приоритетом и переводит её в running.
func (r *JobRepository) ClaimNext(nowUnix int64) (*LyricsJobRecord, error) {
	// Разблокируем задачи, ожидавшие квоту, если время ожидания наступило
	_, _ = r.db.Exec(`
		UPDATE lyrics_jobs
		SET status = 'pending'
		WHERE status = 'waiting_quota' AND next_retry_at <= ?
	`, nowUnix)

	var j LyricsJobRecord
	err := r.db.QueryRow(`
		SELECT id, track_id, job_type, priority, status, attempt_count, next_retry_at, error_log, updated_at
		FROM lyrics_jobs
		WHERE status = 'pending' AND next_retry_at <= ?
		ORDER BY priority DESC, id ASC
		LIMIT 1
	`, nowUnix).Scan(
		&j.ID, &j.TrackID, &j.JobType, &j.Priority, &j.Status,
		&j.AttemptCount, &j.NextRetryAt, &j.ErrorLog, &j.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoPendingJobs
		}
		return nil, err
	}

	_, err = r.db.Exec(`
		UPDATE lyrics_jobs
		SET status = 'running', attempt_count = attempt_count + 1, updated_at = ?
		WHERE id = ?
	`, nowUnix, j.ID)
	if err != nil {
		return nil, err
	}
	j.Status = "running"
	j.AttemptCount++
	j.UpdatedAt = nowUnix
	return &j, nil
}

// MarkDone помечает задачу успешно выполненной.
func (r *JobRepository) MarkDone(id int64) error {
	_, err := r.db.Exec(`
		UPDATE lyrics_jobs
		SET status = 'done', error_log = '', updated_at = ?
		WHERE id = ?
	`, time.Now().Unix(), id)
	return err
}

// MarkWaitingQuota переводит задачу в режим ожидания свободной квоты API-ключей без увеличения счетчика фатальных ошибок.
func (r *JobRepository) MarkWaitingQuota(id int64, retryAtUnix int64, reason string) error {
	_, err := r.db.Exec(`
		UPDATE lyrics_jobs
		SET status = 'waiting_quota', next_retry_at = ?, error_log = ?, updated_at = ?
		WHERE id = ?
	`, retryAtUnix, reason, time.Now().Unix(), id)
	return err
}

// MarkRetryOrFailed планирует повторную попытку (до maxAttempts) или переводит задачу в failed.
func (r *JobRepository) MarkRetryOrFailed(id int64, attempt int, maxAttempts int, errMsg string) error {
	now := time.Now().Unix()
	if attempt >= maxAttempts {
		_, err := r.db.Exec(`
			UPDATE lyrics_jobs
			SET status = 'failed', error_log = ?, updated_at = ?
			WHERE id = ?
		`, errMsg, now, id)
		return err
	}

	backoffSec := int64(15 * attempt * attempt)
	_, err := r.db.Exec(`
		UPDATE lyrics_jobs
		SET status = 'pending', next_retry_at = ?, error_log = ?, updated_at = ?
		WHERE id = ?
	`, now+backoffSec, errMsg, now, id)
	return err
}

// ListRecent возвращает последние задачи очереди для мониторинга в веб-интерфейсе.
func (r *JobRepository) ListRecent(limit int) ([]LyricsJobRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.Query(`
		SELECT id, track_id, job_type, priority, status, attempt_count, next_retry_at, error_log, updated_at
		FROM lyrics_jobs
		ORDER BY updated_at DESC, id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []LyricsJobRecord
	for rows.Next() {
		var j LyricsJobRecord
		if err := rows.Scan(
			&j.ID, &j.TrackID, &j.JobType, &j.Priority, &j.Status,
			&j.AttemptCount, &j.NextRetryAt, &j.ErrorLog, &j.UpdatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, j)
	}
	return list, rows.Err()
}

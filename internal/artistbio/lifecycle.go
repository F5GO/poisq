package artistbio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type LegacyCleanup struct {
	Candidates int64
	Selections int64
}

// CleanupLegacyLocalLibrary removes the deprecated local metadata filler. If
// an external accepted candidate exists it becomes selected in the same
// transaction, otherwise the artist keeps no automatic biography.
func (s *Store) CleanupLegacyLocalLibrary(ctx context.Context) (LegacyCleanup, error) {
	if s == nil || s.db == nil {
		return LegacyCleanup{}, fmt.Errorf("artist biography store is unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LegacyCleanup{}, fmt.Errorf("begin local biography cleanup: %w", err)
	}
	defer tx.Rollback()

	var report LegacyCleanup
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artist_bio_candidates WHERE provider = 'local_library'`).Scan(&report.Candidates); err != nil {
		return LegacyCleanup{}, fmt.Errorf("count local biography candidates: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artist_biographies b
		JOIN artist_bio_candidates c ON c.id = b.automatic_candidate_id AND c.artist_id = b.artist_id
		WHERE c.provider = 'local_library'`).Scan(&report.Selections); err != nil {
		return LegacyCleanup{}, fmt.Errorf("count local biography selections: %w", err)
	}
	if report.Selections > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE artist_biographies AS b SET
			automatic_candidate_id = (
				SELECT c.id FROM artist_bio_candidates c
				WHERE c.artist_id = b.artist_id AND c.status = 'accepted' AND c.provider != 'local_library'
				ORDER BY CASE c.provider
					WHEN 'ai_summary' THEN 9 WHEN 'official' THEN 8 WHEN 'yandex_music' THEN 7
					WHEN 'wikipedia' THEN 6 WHEN 'apple_music' THEN 5 WHEN 'deezer' THEN 4
					WHEN 'theaudiodb' THEN 3 WHEN 'lastfm' THEN 2 WHEN 'discogs' THEN 1 ELSE 0 END DESC,
					c.match_confidence DESC, c.updated_at DESC, c.id ASC LIMIT 1
			), updated_at = ?
			WHERE b.automatic_candidate_id IN (SELECT id FROM artist_bio_candidates WHERE provider = 'local_library')`, time.Now().Unix()); err != nil {
			return LegacyCleanup{}, fmt.Errorf("replace local biography selections: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM artist_bio_candidates WHERE provider = 'local_library'`); err != nil {
		return LegacyCleanup{}, fmt.Errorf("delete local biography candidates: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return LegacyCleanup{}, fmt.Errorf("commit local biography cleanup: %w", err)
	}
	return report, nil
}

func (s *Store) JobState(ctx context.Context, artistID string) (JobState, bool, error) {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return JobState{}, false, err
	}
	var state JobState
	var nextRetryAt, requestedAt, startedAt, completedAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT artist_id, status, input_version, last_error_code,
		next_retry_at, requested_at, started_at, completed_at, updated_at
		FROM artist_bio_jobs WHERE artist_id = ?`, artistID).Scan(
		&state.ArtistID, &state.Status, &state.InputVersion, &state.LastError,
		&nextRetryAt, &requestedAt, &startedAt, &completedAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return JobState{}, false, nil
	}
	if err != nil {
		return JobState{}, false, fmt.Errorf("read biography job state: %w", err)
	}
	if !state.Status.valid() {
		return JobState{}, false, fmt.Errorf("invalid stored biography job status")
	}
	state.NextRetryAt = unixTime(nextRetryAt)
	state.RequestedAt = unixTime(requestedAt)
	state.StartedAt = unixTime(startedAt)
	state.CompletedAt = unixTime(completedAt)
	state.UpdatedAt = unixTime(updatedAt)
	return state, true, nil
}

// QueueJob records admission before any external operation. Terminal results
// and active jobs are not re-admitted unless force is explicitly requested.
func (s *Store) QueueJob(ctx context.Context, artistID, artistName string, force bool, requestedAt time.Time) (JobState, bool, error) {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return JobState{}, false, err
	}
	if err := validateOptional("artist name", artistName, maxArtistNameBytes); err != nil {
		return JobState{}, false, err
	}
	now := unixOrNow(requestedAt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return JobState{}, false, fmt.Errorf("begin biography job admission: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO artist_bio_identity
		(artist_id, artist_name, status, evidence_json, updated_at) VALUES (?, ?, 'pending', '{}', ?)
		ON CONFLICT(artist_id) DO UPDATE SET artist_name = CASE
			WHEN TRIM(artist_bio_identity.artist_name) = '' THEN excluded.artist_name ELSE artist_bio_identity.artist_name END`,
		artistID, strings.TrimSpace(artistName), now); err != nil {
		return JobState{}, false, fmt.Errorf("ensure biography job identity: %w", err)
	}

	var status JobStatus
	var nextRetry int64
	err = tx.QueryRowContext(ctx, `SELECT status, next_retry_at FROM artist_bio_jobs WHERE artist_id = ?`, artistID).Scan(&status, &nextRetry)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return JobState{}, false, fmt.Errorf("read biography job admission state: %w", err)
	}
	admitted := errors.Is(err, sql.ErrNoRows) || force
	if err == nil {
		switch status {
		case JobQueued, JobRunning, JobReady, JobNoData:
			admitted = force && status != JobQueued && status != JobRunning
		case JobRetryable:
			admitted = force || nextRetry <= now
		default:
			return JobState{}, false, fmt.Errorf("invalid stored biography job status")
		}
	}
	if admitted {
		if force {
			if _, err := tx.ExecContext(ctx, `DELETE FROM artist_bio_web_cache WHERE artist_id = ?`, artistID); err != nil {
				return JobState{}, false, fmt.Errorf("invalidate artist web evidence: %w", err)
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO artist_bio_jobs
			(artist_id, status, input_version, last_error_code, next_retry_at, requested_at, started_at, completed_at, updated_at)
			VALUES (?, 'queued', '', '', 0, ?, 0, 0, ?)
			ON CONFLICT(artist_id) DO UPDATE SET status = 'queued', input_version = '',
				last_error_code = '', next_retry_at = 0, requested_at = excluded.requested_at,
				started_at = 0, completed_at = 0, updated_at = excluded.updated_at`, artistID, now, now)
		if err != nil {
			return JobState{}, false, fmt.Errorf("queue biography job: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return JobState{}, false, fmt.Errorf("commit biography job admission: %w", err)
	}
	state, found, err := s.JobState(ctx, artistID)
	if err != nil || !found {
		return JobState{}, false, err
	}
	return state, admitted, nil
}

func (s *Store) MarkJobRunning(ctx context.Context, artistID string, startedAt time.Time) error {
	return s.transitionJob(ctx, artistID, JobRunning, "", "", time.Time{}, startedAt, time.Time{}, []JobStatus{JobQueued})
}

func (s *Store) CompleteNoData(ctx context.Context, artistID, inputVersion string, completedAt time.Time) error {
	return s.transitionJob(ctx, artistID, JobNoData, inputVersion, "", time.Time{}, time.Time{}, completedAt, []JobStatus{JobRunning})
}

func (s *Store) CompleteReady(ctx context.Context, artistID, inputVersion string, completedAt time.Time) error {
	return s.transitionJob(ctx, artistID, JobReady, inputVersion, "", time.Time{}, time.Time{}, completedAt, []JobStatus{JobRunning})
}

func (s *Store) MarkJobRetryable(ctx context.Context, artistID, errorCode string, nextRetryAt, completedAt time.Time) error {
	return s.transitionJob(ctx, artistID, JobRetryable, "", errorCode, nextRetryAt, time.Time{}, completedAt, []JobStatus{JobQueued, JobRunning})
}

func (s *Store) transitionJob(ctx context.Context, artistID string, status JobStatus, version, errorCode string, nextRetryAt, startedAt, completedAt time.Time, from []JobStatus) error {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return err
	}
	if !status.valid() || len(from) == 0 {
		return fmt.Errorf("invalid biography job transition")
	}
	if err := validateOptional("input version", version, maxVersionBytes); err != nil {
		return err
	}
	if err := validateJobErrorCode(errorCode); err != nil {
		return err
	}
	placeholders := make([]string, len(from))
	startedUnix := unixOrZero(startedAt)
	args := []any{
		status, version, errorCode, unixOrZero(nextRetryAt), startedUnix, startedUnix,
		unixOrZero(completedAt), time.Now().Unix(), artistID,
	}
	for i, value := range from {
		if !value.valid() {
			return fmt.Errorf("invalid biography job source status")
		}
		placeholders[i] = "?"
		args = append(args, value)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE artist_bio_jobs SET status = ?, input_version = ?,
		last_error_code = ?, next_retry_at = ?, started_at = CASE WHEN ? = 0 THEN started_at ELSE ? END,
		completed_at = ?, updated_at = ? WHERE artist_id = ? AND status IN (`+strings.Join(placeholders, ",")+`)`,
		args...)
	if err != nil {
		return fmt.Errorf("transition biography job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read biography job transition result: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("biography job transition rejected")
	}
	return nil
}

// CommitAISummary atomically stores and selects a grounded summary and marks
// its detached job ready. Manual columns are intentionally untouched.
func (s *Store) CommitAISummary(ctx context.Context, artistID, inputVersion, text, evidenceJSON string, completedAt time.Time) (int64, error) {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return 0, err
	}
	if err := validateRequired("input version", inputVersion, maxVersionBytes); err != nil {
		return 0, err
	}
	if err := validateRequired("AI summary", text, MaxBiographyBytes); err != nil {
		return 0, err
	}
	if err := validateEvidence(evidenceJSON); err != nil {
		return 0, err
	}
	analysis := DetectBiographyLanguage(text)
	if !analysis.QualityOK || analysis.Language != "ru" || analysis.Confidence < 0.90 || analysis.RussianShare < 0.80 {
		return 0, fmt.Errorf("AI summary is not verified russian text")
	}
	now := unixOrNow(completedAt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin AI summary commit: %w", err)
	}
	defer tx.Rollback()
	var candidateID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO artist_bio_candidates
		(artist_id, provider, source_artist_id, original_text, original_language, detected_language,
		 language_confidence, russian_share, match_confidence, status, evidence_json, fetched_at, updated_at)
		VALUES (?, 'ai_summary', ?, ?, 'ru', 'ru', ?, ?, 1, 'accepted', ?, ?, ?)
		ON CONFLICT(artist_id, provider, source_artist_id) DO UPDATE SET original_text = excluded.original_text,
			detected_language = excluded.detected_language, language_confidence = excluded.language_confidence,
			russian_share = excluded.russian_share, status = excluded.status,
			evidence_json = excluded.evidence_json, fetched_at = excluded.fetched_at, updated_at = excluded.updated_at
		RETURNING id`, artistID, inputVersion, text, analysis.Confidence, analysis.RussianShare,
		jsonOrEmptyObject(evidenceJSON), now, now).Scan(&candidateID)
	if err != nil {
		return 0, fmt.Errorf("store AI summary: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artist_biographies (artist_id, automatic_candidate_id, updated_at)
		VALUES (?, ?, ?) ON CONFLICT(artist_id) DO UPDATE SET
			automatic_candidate_id = excluded.automatic_candidate_id, updated_at = excluded.updated_at`,
		artistID, candidateID, now); err != nil {
		return 0, fmt.Errorf("select AI summary: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE artist_bio_jobs SET status = 'ready', input_version = ?,
		last_error_code = '', next_retry_at = 0, completed_at = ?, updated_at = ?
		WHERE artist_id = ? AND status = 'running'`, inputVersion, now, now, artistID)
	if err != nil {
		return 0, fmt.Errorf("complete AI summary job: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return 0, fmt.Errorf("AI summary job is not running")
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit AI summary: %w", err)
	}
	return candidateID, nil
}

func (s *Store) RecoverStaleJobs(ctx context.Context, staleBefore, retryAt time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE artist_bio_jobs SET status = 'retryable',
		last_error_code = 'restart_recovery', next_retry_at = ?, completed_at = ?, updated_at = ?
		WHERE status IN ('queued', 'running') AND updated_at <= ?`, retryAt.Unix(), time.Now().Unix(), time.Now().Unix(), staleBefore.Unix())
	if err != nil {
		return 0, fmt.Errorf("recover stale biography jobs: %w", err)
	}
	return result.RowsAffected()
}

func validateJobErrorCode(value string) error {
	if err := validateOptional("job error code", value, maxJobErrorBytes); err != nil {
		return err
	}
	for _, r := range value {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return fmt.Errorf("invalid job error code")
		}
	}
	return nil
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func unixTime(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(value, 0)
}

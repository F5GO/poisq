package artistbio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrCandidateNotFound       = errors.New("artist biography candidate not found")
	ErrCandidateArtistMismatch = errors.New("artist biography candidate belongs to another artist")
	ErrCandidateNotAccepted    = errors.New("artist biography candidate is not accepted")
)

type Store struct {
	db *sql.DB
}

type AutomaticCacheReset struct {
	Identities int64 `json:"identities"`
	Candidates int64 `json:"candidates"`
	Selections int64 `json:"selections"`
	Jobs       int64 `json:"jobs"`
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("artist biography store requires database")
	}
	store := &Store{db: db}
	if _, err := store.CleanupLegacyLocalLibrary(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

// ResetAutomaticCache clears every derived biography result while preserving
// manual overrides. Preserved manual artists receive a fresh pending identity
// so the automatic pipeline can rebuild beneath the override.
func (s *Store) ResetAutomaticCache(ctx context.Context) (AutomaticCacheReset, error) {
	if s == nil || s.db == nil {
		return AutomaticCacheReset{}, fmt.Errorf("artist biography store is unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AutomaticCacheReset{}, fmt.Errorf("begin biography cache reset: %w", err)
	}
	defer tx.Rollback()

	var report AutomaticCacheReset
	for query, target := range map[string]*int64{
		`SELECT COUNT(*) FROM artist_bio_identity`:                                         &report.Identities,
		`SELECT COUNT(*) FROM artist_bio_candidates`:                                       &report.Candidates,
		`SELECT COUNT(*) FROM artist_biographies WHERE automatic_candidate_id IS NOT NULL`: &report.Selections,
		`SELECT COUNT(*) FROM artist_bio_jobs`:                                             &report.Jobs,
	} {
		if err := tx.QueryRowContext(ctx, query).Scan(target); err != nil {
			return AutomaticCacheReset{}, fmt.Errorf("count biography cache state: %w", err)
		}
	}

	now := time.Now().Unix()
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM artist_bio_jobs`, nil},
		{`DELETE FROM artist_bio_web_cache`, nil},
		{`UPDATE artist_biographies SET automatic_candidate_id = NULL, updated_at = ?`, []any{now}},
		{`DELETE FROM artist_bio_candidates`, nil},
		{`DELETE FROM artist_bio_identity WHERE artist_id NOT IN (
			SELECT artist_id FROM artist_biographies WHERE TRIM(manual_text) != ''
		)`, nil},
		{`UPDATE artist_bio_identity SET musicbrainz_id = '', status = 'pending', resolved_source = '',
			resolved_artist_id = '', confidence = 0, evidence_json = '{}', updated_at = 0`, nil},
		{`DELETE FROM sqlite_sequence WHERE name = 'artist_bio_candidates'`, nil},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return AutomaticCacheReset{}, fmt.Errorf("clear biography cache state: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return AutomaticCacheReset{}, fmt.Errorf("commit biography cache reset: %w", err)
	}
	return report, nil
}

func (s *Store) PutIdentity(ctx context.Context, identity ArtistIdentity) error {
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("invalid artist identity: %w", err)
	}
	updatedAt := unixOrNow(identity.UpdatedAt)
	evidence := jsonOrEmptyObject(identity.EvidenceJSON)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO artist_bio_identity (
			artist_id, artist_name, musicbrainz_id, status, resolved_source,
			resolved_artist_id, confidence, evidence_json, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(artist_id) DO UPDATE SET
			artist_name = excluded.artist_name,
			musicbrainz_id = excluded.musicbrainz_id,
			status = excluded.status,
			resolved_source = excluded.resolved_source,
			resolved_artist_id = excluded.resolved_artist_id,
			confidence = excluded.confidence,
			evidence_json = excluded.evidence_json,
			updated_at = excluded.updated_at`,
		identity.ArtistID, identity.ArtistName, identity.MusicBrainzID, identity.Status,
		identity.ResolvedSource, identity.ResolvedArtistID, identity.Confidence, evidence, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("store artist identity: %w", err)
	}
	return nil
}

func (s *Store) Identity(ctx context.Context, artistID string) (ArtistIdentity, bool, error) {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return ArtistIdentity{}, false, err
	}
	var identity ArtistIdentity
	var updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT
		artist_id, artist_name, musicbrainz_id, status, resolved_source,
		resolved_artist_id, confidence, evidence_json, updated_at
		FROM artist_bio_identity WHERE artist_id = ?`, artistID).Scan(
		&identity.ArtistID, &identity.ArtistName, &identity.MusicBrainzID, &identity.Status,
		&identity.ResolvedSource, &identity.ResolvedArtistID, &identity.Confidence,
		&identity.EvidenceJSON, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtistIdentity{}, false, nil
	}
	if err != nil {
		return ArtistIdentity{}, false, fmt.Errorf("read artist identity: %w", err)
	}
	identity.UpdatedAt = time.Unix(updatedAt, 0)
	return identity, true, nil
}

func (s *Store) PutCandidate(ctx context.Context, candidate Candidate) (int64, error) {
	if err := candidate.Validate(); err != nil {
		return 0, fmt.Errorf("invalid biography candidate: %w", err)
	}
	fetchedAt := unixOrNow(candidate.FetchedAt)
	updatedAt := unixOrNow(candidate.UpdatedAt)
	evidence := jsonOrEmptyObject(candidate.EvidenceJSON)

	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO artist_bio_candidates (
			artist_id, provider, source_artist_id, source_url, original_text, russian_text,
			original_language, detected_language, language_confidence, russian_share,
			match_confidence, status, evidence_json, fetched_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(artist_id, provider, source_artist_id) DO UPDATE SET
			source_url = CASE WHEN artist_bio_candidates.id IN (SELECT automatic_candidate_id FROM artist_biographies) THEN artist_bio_candidates.source_url ELSE excluded.source_url END,
			original_text = CASE WHEN artist_bio_candidates.id IN (SELECT automatic_candidate_id FROM artist_biographies) THEN artist_bio_candidates.original_text ELSE excluded.original_text END,
			russian_text = CASE WHEN artist_bio_candidates.id IN (SELECT automatic_candidate_id FROM artist_biographies) THEN artist_bio_candidates.russian_text ELSE excluded.russian_text END,
			original_language = CASE WHEN artist_bio_candidates.id IN (SELECT automatic_candidate_id FROM artist_biographies) THEN artist_bio_candidates.original_language ELSE excluded.original_language END,
			detected_language = CASE WHEN artist_bio_candidates.id IN (SELECT automatic_candidate_id FROM artist_biographies) THEN artist_bio_candidates.detected_language ELSE excluded.detected_language END,
			language_confidence = CASE WHEN artist_bio_candidates.id IN (SELECT automatic_candidate_id FROM artist_biographies) THEN artist_bio_candidates.language_confidence ELSE excluded.language_confidence END,
			russian_share = CASE WHEN artist_bio_candidates.id IN (SELECT automatic_candidate_id FROM artist_biographies) THEN artist_bio_candidates.russian_share ELSE excluded.russian_share END,
			match_confidence = CASE WHEN artist_bio_candidates.id IN (SELECT automatic_candidate_id FROM artist_biographies) THEN artist_bio_candidates.match_confidence ELSE excluded.match_confidence END,
			status = CASE WHEN artist_bio_candidates.id IN (SELECT automatic_candidate_id FROM artist_biographies) THEN artist_bio_candidates.status ELSE excluded.status END,
			evidence_json = CASE WHEN artist_bio_candidates.id IN (SELECT automatic_candidate_id FROM artist_biographies) THEN artist_bio_candidates.evidence_json ELSE excluded.evidence_json END,
			fetched_at = excluded.fetched_at,
			updated_at = excluded.updated_at
		RETURNING id`,
		candidate.ArtistID, candidate.Provider, candidate.SourceArtistID, candidate.SourceURL,
		candidate.OriginalText, candidate.RussianText, candidate.OriginalLanguage,
		candidate.DetectedLanguage, candidate.LanguageConfidence, candidate.RussianShare,
		candidate.MatchConfidence, candidate.Status, evidence, fetchedAt, updatedAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store biography candidate: %w", err)
	}
	return id, nil
}

// CandidateValidation contains only fields that validation is allowed to
// change. Original source text and source identity remain immutable here.
type CandidateValidation struct {
	ArtistID           string
	CandidateID        int64
	RussianText        string
	DetectedLanguage   string
	LanguageConfidence float64
	RussianShare       float64
	Status             CandidateStatus
	EvidenceJSON       string
	UpdatedAt          time.Time
}

// Candidates returns one deterministic bounded page for background validation.
func (s *Store) Candidates(ctx context.Context, artistID string, limit int) ([]Candidate, error) {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 64 {
		return nil, fmt.Errorf("candidate limit must be between 1 and 64")
	}
	rows, err := s.db.QueryContext(ctx, candidateSelectSQL+`
		WHERE c.artist_id = ?
		ORDER BY c.status = 'accepted', c.match_confidence DESC, c.id ASC
		LIMIT ?`, artistID, limit)
	if err != nil {
		return nil, fmt.Errorf("list biography candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]Candidate, 0, limit)
	for rows.Next() {
		candidate, err := scanCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("scan biography candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list biography candidates: %w", err)
	}
	return candidates, nil
}

// ApplyValidationAndSelect updates one candidate and chooses the best accepted
// automatic result in the same transaction. Manual override columns are never
// read or changed by this operation.
func (s *Store) ApplyValidationAndSelect(ctx context.Context, validation CandidateValidation) error {
	if err := validateCandidateValidation(validation); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin biography validation: %w", err)
	}
	defer tx.Rollback()

	var owner, originalText string
	err = tx.QueryRowContext(ctx, `SELECT artist_id, original_text FROM artist_bio_candidates WHERE id = ?`, validation.CandidateID).Scan(&owner, &originalText)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCandidateNotFound
	}
	if err != nil {
		return fmt.Errorf("read biography candidate for validation: %w", err)
	}
	if owner != validation.ArtistID {
		return ErrCandidateArtistMismatch
	}
	if validation.Status == CandidateAccepted {
		text := originalText
		if validation.RussianText != "" {
			text = validation.RussianText
		}
		analysis := DetectBiographyLanguage(text)
		if !analysis.QualityOK || analysis.Language != "ru" || analysis.Confidence < 0.90 || analysis.RussianShare < 0.80 {
			return fmt.Errorf("accepted biography is not verified russian text")
		}
	}
	updatedAt := unixOrNow(validation.UpdatedAt)
	result, err := tx.ExecContext(ctx, `UPDATE artist_bio_candidates SET
		russian_text = ?, detected_language = ?, language_confidence = ?,
		russian_share = ?, status = ?, evidence_json = ?, updated_at = ?
		WHERE id = ? AND artist_id = ?`,
		validation.RussianText, validation.DetectedLanguage, validation.LanguageConfidence,
		validation.RussianShare, validation.Status, jsonOrEmptyObject(validation.EvidenceJSON),
		updatedAt, validation.CandidateID, validation.ArtistID)
	if err != nil {
		return fmt.Errorf("update biography validation: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return ErrCandidateNotFound
	}

	var selectedID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM artist_bio_candidates
		WHERE artist_id = ? AND status = 'accepted'
		ORDER BY
			CASE WHEN provider = 'ai_summary' THEN 0 WHEN provider = 'local_library' THEN 2 ELSE 1 END ASC,
			match_confidence DESC,
			CASE WHEN russian_text = '' THEN 1 ELSE 0 END DESC,
			language_confidence DESC,
			russian_share DESC,
			CASE provider
				WHEN 'official' THEN 8 WHEN 'yandex_music' THEN 7 WHEN 'wikipedia' THEN 6
				WHEN 'apple_music' THEN 5 WHEN 'deezer' THEN 4 WHEN 'theaudiodb' THEN 3
				WHEN 'lastfm' THEN 2 WHEN 'discogs' THEN 1 ELSE 0 END DESC,
			MIN(LENGTH(CASE WHEN russian_text = '' THEN original_text ELSE russian_text END), 8000) DESC,
			updated_at DESC, id ASC
		LIMIT 1`, validation.ArtistID).Scan(&selectedID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("rank accepted biography candidates: %w", err)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO artist_biographies (artist_id, automatic_candidate_id, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(artist_id) DO UPDATE SET
				automatic_candidate_id = excluded.automatic_candidate_id,
				updated_at = excluded.updated_at`, validation.ArtistID, selectedID, updatedAt)
		if err != nil {
			return fmt.Errorf("select validated biography: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit biography validation: %w", err)
	}
	return nil
}

func validateCandidateValidation(value CandidateValidation) error {
	if err := validateRequired("artist id", value.ArtistID, MaxProviderIDBytes); err != nil {
		return err
	}
	if value.CandidateID <= 0 {
		return ErrCandidateNotFound
	}
	if err := validateOptional("russian biography", value.RussianText, MaxBiographyBytes); err != nil {
		return err
	}
	if !validBiographyLanguage(value.DetectedLanguage) {
		return fmt.Errorf("invalid detected biography language")
	}
	if err := validateConfidence("language confidence", value.LanguageConfidence); err != nil {
		return err
	}
	if err := validateConfidence("russian share", value.RussianShare); err != nil {
		return err
	}
	if value.Status != CandidateAccepted && value.Status != CandidateRejected && value.Status != CandidateTranslationPending {
		return fmt.Errorf("invalid validation status")
	}
	if value.Status != CandidateAccepted && value.RussianText != "" {
		return fmt.Errorf("unaccepted candidate must not store russian text")
	}
	return validateEvidence(value.EvidenceJSON)
}

func (s *Store) Candidate(ctx context.Context, artistID string, provider Provider, sourceArtistID string) (Candidate, bool, error) {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return Candidate{}, false, err
	}
	if !provider.valid() {
		return Candidate{}, false, fmt.Errorf("invalid provider %q", provider)
	}
	if err := validateRequired("source artist id", sourceArtistID, MaxProviderIDBytes); err != nil {
		return Candidate{}, false, err
	}
	row := s.db.QueryRowContext(ctx, candidateSelectSQL+`
		WHERE c.artist_id = ? AND c.provider = ? AND c.source_artist_id = ?`, artistID, provider, sourceArtistID)
	candidate, err := scanCandidate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Candidate{}, false, nil
	}
	if err != nil {
		return Candidate{}, false, fmt.Errorf("read biography candidate: %w", err)
	}
	return candidate, true, nil
}

func (s *Store) SelectAutomatic(ctx context.Context, artistID string, candidateID int64, selectedAt time.Time) error {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return err
	}
	if candidateID <= 0 {
		return ErrCandidateNotFound
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin automatic biography selection: %w", err)
	}
	defer tx.Rollback()

	var owner string
	var status CandidateStatus
	err = tx.QueryRowContext(ctx, `SELECT artist_id, status FROM artist_bio_candidates WHERE id = ?`, candidateID).Scan(&owner, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCandidateNotFound
	}
	if err != nil {
		return fmt.Errorf("read automatic biography candidate: %w", err)
	}
	if owner != artistID {
		return ErrCandidateArtistMismatch
	}
	if status != CandidateAccepted {
		return ErrCandidateNotAccepted
	}

	selectedUnix := unixOrNow(selectedAt)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO artist_biographies (artist_id, automatic_candidate_id, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(artist_id) DO UPDATE SET
			automatic_candidate_id = excluded.automatic_candidate_id,
			updated_at = excluded.updated_at`, artistID, candidateID, selectedUnix)
	if err != nil {
		return fmt.Errorf("select automatic biography: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit automatic biography selection: %w", err)
	}
	return nil
}

func (s *Store) AutomaticCandidate(ctx context.Context, artistID string) (Candidate, bool, error) {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return Candidate{}, false, err
	}
	row := s.db.QueryRowContext(ctx, candidateSelectSQL+`
		JOIN artist_biographies b ON b.automatic_candidate_id = c.id
		WHERE b.artist_id = ?`, artistID)
	candidate, err := scanCandidate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Candidate{}, false, nil
	}
	if err != nil {
		return Candidate{}, false, fmt.Errorf("read automatic biography: %w", err)
	}
	return candidate, true, nil
}

func (s *Store) SetManualOverride(ctx context.Context, override ManualOverride) error {
	if err := override.Validate(); err != nil {
		return fmt.Errorf("invalid manual biography override: %w", err)
	}
	text := override.Text
	sourceURL := override.SourceURL
	manualUpdatedAt := unixOrNow(override.UpdatedAt)
	if strings.TrimSpace(text) == "" {
		text = ""
		sourceURL = ""
		manualUpdatedAt = 0
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO artist_biographies (
			artist_id, manual_text, manual_source_url, manual_updated_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(artist_id) DO UPDATE SET
			manual_text = excluded.manual_text,
			manual_source_url = excluded.manual_source_url,
			manual_updated_at = excluded.manual_updated_at,
			updated_at = excluded.updated_at`,
		override.ArtistID, text, sourceURL, manualUpdatedAt, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("store manual biography override: %w", err)
	}
	return nil
}

func (s *Store) EffectiveBiography(ctx context.Context, artistID string) (EffectiveBiography, bool, error) {
	if err := validateRequired("artist id", artistID, MaxProviderIDBytes); err != nil {
		return EffectiveBiography{}, false, err
	}

	var result EffectiveBiography
	var manualText, manualURL string
	var manualUpdatedAt, selectedUpdatedAt int64
	var candidateID, candidateUpdatedAt int64
	var provider Provider
	var sourceURL, originalText, russianText string
	err := s.db.QueryRowContext(ctx, effectiveBiographySQL, artistID).Scan(
		&result.ArtistID, &manualText, &manualURL, &manualUpdatedAt, &selectedUpdatedAt,
		&candidateID, &provider, &sourceURL, &originalText, &russianText, &candidateUpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return EffectiveBiography{}, false, nil
	}
	if err != nil {
		return EffectiveBiography{}, false, fmt.Errorf("read effective biography: %w", err)
	}

	if strings.TrimSpace(manualText) != "" {
		result.Text = manualText
		result.Source = ProviderManual
		result.SourceURL = manualURL
		result.Manual = true
		result.UpdatedAt = time.Unix(manualUpdatedAt, 0)
		return result, true, nil
	}
	if candidateID == 0 {
		return EffectiveBiography{}, false, nil
	}

	result.Text = originalText
	if strings.TrimSpace(russianText) != "" {
		result.Text = russianText
		result.Translated = true
	}
	if strings.TrimSpace(result.Text) == "" {
		return EffectiveBiography{}, false, nil
	}
	result.Source = provider
	result.SourceURL = sourceURL
	result.CandidateID = candidateID
	result.UpdatedAt = time.Unix(candidateUpdatedAt, 0)
	return result, true, nil
}

const effectiveBiographySQL = `SELECT
	b.artist_id, b.manual_text, b.manual_source_url, b.manual_updated_at, b.updated_at,
	COALESCE(c.id, 0), COALESCE(c.provider, ''), COALESCE(c.source_url, ''),
	COALESCE(c.original_text, ''), COALESCE(c.russian_text, ''), COALESCE(c.updated_at, 0)
	FROM artist_biographies b
	LEFT JOIN artist_bio_candidates c
		ON c.id = b.automatic_candidate_id AND c.artist_id = b.artist_id AND c.status = 'accepted'
	WHERE b.artist_id = ?`

const candidateSelectSQL = `SELECT
	c.id, c.artist_id, c.provider, c.source_artist_id, c.source_url, c.original_text, c.russian_text,
	c.original_language, c.detected_language, c.language_confidence, c.russian_share,
	c.match_confidence, c.status, c.evidence_json, c.fetched_at, c.updated_at
	FROM artist_bio_candidates c `

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCandidate(row rowScanner) (Candidate, error) {
	var candidate Candidate
	var fetchedAt, updatedAt int64
	err := row.Scan(
		&candidate.ID, &candidate.ArtistID, &candidate.Provider, &candidate.SourceArtistID,
		&candidate.SourceURL, &candidate.OriginalText, &candidate.RussianText,
		&candidate.OriginalLanguage, &candidate.DetectedLanguage, &candidate.LanguageConfidence,
		&candidate.RussianShare, &candidate.MatchConfidence, &candidate.Status,
		&candidate.EvidenceJSON, &fetchedAt, &updatedAt,
	)
	if err != nil {
		return Candidate{}, err
	}
	candidate.FetchedAt = time.Unix(fetchedAt, 0)
	candidate.UpdatedAt = time.Unix(updatedAt, 0)
	return candidate, nil
}

func unixOrNow(value time.Time) int64 {
	if value.IsZero() {
		return time.Now().Unix()
	}
	return value.Unix()
}

func jsonOrEmptyObject(value string) string {
	if value == "" {
		return "{}"
	}
	return value
}

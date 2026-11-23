package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/musicmeta"
)

type ArtworkRecord struct {
	Scope     string `json:"scope"`
	ScopeID   string `json:"scopeId"`
	Role      string `json:"role"`
	FilePath  string `json:"filePath"`
	Source    string `json:"source"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Score     int    `json:"score"`
	UpdatedAt int64  `json:"updatedAt"`
}

type SimilarArtistRecord struct {
	ReferenceMBID string  `json:"referenceMbid"`
	SimilarMBID   string  `json:"similarMbid"`
	Name          string  `json:"name"`
	Score         float64 `json:"score"`
	ListenCount   int64   `json:"listenCount"`
	Provider      string  `json:"provider"`
	FetchedAt     int64   `json:"fetchedAt"`
}

type MediaEnrichmentRepository struct{ db *sql.DB }

func NewMediaEnrichmentRepository(database *sql.DB) *MediaEnrichmentRepository {
	return &MediaEnrichmentRepository{db: database}
}

func (r *MediaEnrichmentRepository) PutArtwork(ctx context.Context, value ArtworkRecord) error {
	if (value.Scope != "album" && value.Scope != "artist") || strings.TrimSpace(value.ScopeID) == "" || !validArtworkRole(value.Role) || strings.TrimSpace(value.FilePath) == "" || strings.TrimSpace(value.Source) == "" || value.Width <= 0 || value.Height <= 0 {
		return fmt.Errorf("invalid artwork record")
	}
	now := time.Now().UTC().Unix()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO media_artwork(scope,scope_id,role,file_path,source,width,height,score,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(scope,scope_id,role) DO UPDATE SET file_path=excluded.file_path,source=excluded.source,
			width=excluded.width,height=excluded.height,score=excluded.score,updated_at=excluded.updated_at`,
		value.Scope, value.ScopeID, value.Role, value.FilePath, value.Source, value.Width, value.Height, value.Score, now)
	return err
}

func (r *MediaEnrichmentRepository) ListArtwork(ctx context.Context, scope, scopeID string) ([]ArtworkRecord, error) {
	if (scope != "album" && scope != "artist") || strings.TrimSpace(scopeID) == "" {
		return nil, fmt.Errorf("invalid artwork scope")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT scope,scope_id,role,file_path,source,width,height,score,updated_at
		FROM media_artwork WHERE scope=? AND scope_id=? ORDER BY role`, scope, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []ArtworkRecord
	for rows.Next() {
		var value ArtworkRecord
		if err := rows.Scan(&value.Scope, &value.ScopeID, &value.Role, &value.FilePath, &value.Source, &value.Width, &value.Height, &value.Score, &value.UpdatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *MediaEnrichmentRepository) DeleteArtwork(ctx context.Context, scope, scopeID, role string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM media_artwork WHERE scope=? AND scope_id=? AND role=?`, scope, scopeID, role)
	return err
}

func (r *MediaEnrichmentRepository) ReplaceSimilarArtists(ctx context.Context, referenceMBID string, input []SimilarArtistRecord) error {
	if !musicmeta.ValidMusicBrainzID(referenceMBID) || len(input) > 100 {
		return fmt.Errorf("invalid similar-artist replacement")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM similar_artists WHERE reference_mbid=?`, referenceMBID); err != nil {
		return err
	}
	now := time.Now().UTC().Unix()
	seen := make(map[string]struct{}, len(input))
	for _, value := range input {
		if value.ReferenceMBID != referenceMBID || !musicmeta.ValidMusicBrainzID(value.SimilarMBID) || value.SimilarMBID == referenceMBID || strings.TrimSpace(value.Name) == "" || math.IsNaN(value.Score) || math.IsInf(value.Score, 0) || value.Score < 0 || value.Score > 1 || value.ListenCount < 0 {
			return fmt.Errorf("invalid similar artist")
		}
		if _, duplicate := seen[value.SimilarMBID]; duplicate {
			return fmt.Errorf("duplicate similar artist")
		}
		seen[value.SimilarMBID] = struct{}{}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO similar_artists(reference_mbid,similar_mbid,name,score,listen_count,provider,fetched_at)
			VALUES(?,?,?,?,?,'listenbrainz',?)`, referenceMBID, value.SimilarMBID, value.Name, value.Score, value.ListenCount, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *MediaEnrichmentRepository) ListSimilarArtists(ctx context.Context, referenceMBID string, limit int) ([]SimilarArtistRecord, error) {
	if !musicmeta.ValidMusicBrainzID(referenceMBID) {
		return nil, fmt.Errorf("invalid MusicBrainz artist id")
	}
	if limit < 1 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT reference_mbid,similar_mbid,name,score,listen_count,provider,fetched_at
		FROM similar_artists WHERE reference_mbid=? ORDER BY score DESC,listen_count DESC,similar_mbid LIMIT ?`, referenceMBID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []SimilarArtistRecord
	for rows.Next() {
		var value SimilarArtistRecord
		if err := rows.Scan(&value.ReferenceMBID, &value.SimilarMBID, &value.Name, &value.Score, &value.ListenCount, &value.Provider, &value.FetchedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func validArtworkRole(role string) bool {
	switch role {
	case "album_primary", "artist_primary", "disc", "logo", "banner", "backdrop":
		return true
	default:
		return false
	}
}

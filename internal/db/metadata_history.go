package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/F5GO/poisq/internal/musicmeta"
)

type MetadataHistoryRecord struct {
	ID                     int64           `json:"id"`
	TrackID                string          `json:"trackId"`
	ProposalID             int64           `json:"proposalId"`
	WritebackTransactionID string          `json:"writebackTransactionId"`
	Field                  string          `json:"field"`
	OldValue               json.RawMessage `json:"oldValue"`
	NewValue               json.RawMessage `json:"newValue"`
	Source                 string          `json:"source"`
	Provider               string          `json:"provider,omitempty"`
	Actor                  string          `json:"actor"`
	CreatedAt              int64           `json:"createdAt"`
	RevertedByProposalID   *int64          `json:"revertedByProposalId,omitempty"`
}

type MetadataHistoryRepository struct{ db *sql.DB }

func NewMetadataHistoryRepository(database *sql.DB) *MetadataHistoryRepository {
	return &MetadataHistoryRepository{db: database}
}

func (r *MetadataHistoryRepository) Append(ctx context.Context, trackID string, proposalID int64, transactionID, source, provider, actor string, changes []musicmeta.FieldChange) error {
	if strings.TrimSpace(trackID) == "" || proposalID <= 0 || strings.TrimSpace(transactionID) == "" {
		return fmt.Errorf("invalid metadata history identity")
	}
	if len(changes) > 256 {
		return fmt.Errorf("metadata history change limit exceeded")
	}
	if actor = strings.TrimSpace(actor); actor == "" {
		actor = "system"
	}
	if len(actor) > 128 || len(source) > 128 || len(provider) > 512 {
		return fmt.Errorf("metadata history label exceeds limit")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	createdAt := time.Now().Unix()
	for _, change := range changes {
		if !validHistoryField(change.Field) || len(change.Before) > 64<<10 || len(change.After) > 64<<10 || !json.Valid(change.Before) || !json.Valid(change.After) {
			return fmt.Errorf("invalid metadata history field %q", change.Field)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO metadata_field_history(track_id,proposal_id,writeback_transaction_id,field,old_value_json,new_value_json,source,provider,actor,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(proposal_id,field) DO NOTHING`,
			trackID, proposalID, transactionID, change.Field, string(change.Before), string(change.After), source, provider, actor, createdAt)
		if err != nil {
			return fmt.Errorf("append metadata field history: %w", err)
		}
	}
	return tx.Commit()
}

func validHistoryField(field string) bool {
	if strings.HasPrefix(field, "custom:") {
		return len(field) > len("custom:") && len(field) <= 256
	}
	return musicmeta.ValidField(musicmeta.Field(field)) && musicmeta.Field(field) != musicmeta.FieldCustomTags
}

func (r *MetadataHistoryRepository) List(ctx context.Context, trackID, field string, beforeCreatedAt, beforeID int64, limit int) ([]MetadataHistoryRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	query := `SELECT id,track_id,proposal_id,writeback_transaction_id,field,old_value_json,new_value_json,source,provider,actor,created_at,reverted_by_proposal_id FROM metadata_field_history WHERE 1=1`
	args := []any{}
	if trackID != "" {
		query += ` AND track_id=?`
		args = append(args, trackID)
	}
	if field != "" {
		if !validHistoryField(field) {
			return nil, fmt.Errorf("invalid history field")
		}
		query += ` AND field=?`
		args = append(args, field)
	}
	if beforeCreatedAt > 0 || beforeID > 0 {
		if beforeCreatedAt <= 0 || beforeID <= 0 {
			return nil, fmt.Errorf("invalid history cursor")
		}
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, beforeCreatedAt, beforeCreatedAt, beforeID)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetadataHistoryRecord
	for rows.Next() {
		var value MetadataHistoryRecord
		var oldJSON, newJSON string
		var reverted sql.NullInt64
		if err := rows.Scan(&value.ID, &value.TrackID, &value.ProposalID, &value.WritebackTransactionID, &value.Field, &oldJSON, &newJSON, &value.Source, &value.Provider, &value.Actor, &value.CreatedAt, &reverted); err != nil {
			return nil, err
		}
		value.OldValue, value.NewValue = json.RawMessage(oldJSON), json.RawMessage(newJSON)
		if reverted.Valid {
			id := reverted.Int64
			value.RevertedByProposalID = &id
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (r *MetadataHistoryRepository) Get(ctx context.Context, id int64) (MetadataHistoryRecord, error) {
	rows, err := r.ListByIDs(ctx, []int64{id})
	if err != nil {
		return MetadataHistoryRecord{}, err
	}
	if len(rows) != 1 {
		return MetadataHistoryRecord{}, sql.ErrNoRows
	}
	return rows[0], nil
}

func (r *MetadataHistoryRepository) ListByIDs(ctx context.Context, ids []int64) ([]MetadataHistoryRecord, error) {
	if len(ids) == 0 || len(ids) > 200 {
		return nil, fmt.Errorf("invalid history id count")
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("invalid history id")
		}
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT id,track_id,proposal_id,writeback_transaction_id,field,old_value_json,new_value_json,source,provider,actor,created_at,reverted_by_proposal_id FROM metadata_field_history WHERE id IN (` + strings.Join(placeholders, ",") + `) ORDER BY id`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetadataHistoryRecord
	for rows.Next() {
		var value MetadataHistoryRecord
		var oldJSON, newJSON string
		var reverted sql.NullInt64
		if err := rows.Scan(&value.ID, &value.TrackID, &value.ProposalID, &value.WritebackTransactionID, &value.Field, &oldJSON, &newJSON, &value.Source, &value.Provider, &value.Actor, &value.CreatedAt, &reverted); err != nil {
			return nil, err
		}
		value.OldValue = json.RawMessage(oldJSON)
		value.NewValue = json.RawMessage(newJSON)
		if reverted.Valid {
			id := reverted.Int64
			value.RevertedByProposalID = &id
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (r *MetadataHistoryRepository) MarkReverted(ctx context.Context, ids []int64, proposalID int64) error {
	if len(ids) == 0 || len(ids) > 200 || proposalID <= 0 {
		return fmt.Errorf("invalid history revert")
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, proposalID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	_, err := r.db.ExecContext(ctx, `UPDATE metadata_field_history SET reverted_by_proposal_id=? WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	return err
}

func (r *MetadataHistoryRepository) LinkRevert(ctx context.Context, ids []int64, proposalID int64) error {
	if len(ids) == 0 || len(ids) > 200 || proposalID <= 0 {
		return fmt.Errorf("invalid history revert link")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if id <= 0 {
			return fmt.Errorf("invalid history id")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO metadata_history_revert_links(history_id,proposal_id) VALUES(?,?)`, id, proposalID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *MetadataHistoryRepository) CompleteLinkedRevert(ctx context.Context, proposalID int64) error {
	if proposalID <= 0 {
		return fmt.Errorf("invalid revert proposal")
	}
	_, err := r.db.ExecContext(ctx, `UPDATE metadata_field_history SET reverted_by_proposal_id=? WHERE id IN (SELECT history_id FROM metadata_history_revert_links WHERE proposal_id=?)`, proposalID, proposalID)
	return err
}

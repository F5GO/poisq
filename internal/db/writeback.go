package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const MaxWritebackErrorBytes = 2 << 10

type WritebackTransaction struct {
	TransactionID  string `json:"transactionId"`
	ProposalID     int64  `json:"proposalId"`
	Kind           string `json:"kind"`
	TargetPath     string `json:"targetPath"`
	BackupPath     string `json:"backupPath"`
	OriginalExists bool   `json:"originalExists"`
	OriginalSize   int64  `json:"originalSize"`
	OriginalSHA256 string `json:"originalSha256"`
	OriginalJSON   string `json:"originalJson"`
	State          string `json:"state"`
	Reconciled     bool   `json:"reconciled"`
	LastError      string `json:"lastError"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
}

type WritebackRepository struct{ db *sql.DB }

func NewWritebackRepository(database *sql.DB) *WritebackRepository {
	return &WritebackRepository{db: database}
}

func (r *StagingRepository) WritebackRepository() *WritebackRepository {
	return NewWritebackRepository(r.db)
}

func (r *WritebackRepository) CreatePrepared(ctx context.Context, input WritebackTransaction) error {
	if err := validateWriteback(input); err != nil {
		return err
	}
	now := time.Now().UTC().Unix()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO writeback_transactions(
			transaction_id,proposal_id,kind,target_path,backup_path,original_exists,original_size,
			original_sha256,original_json,state,reconciled,last_error,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,'prepared',0,'',?,?)`,
		input.TransactionID, input.ProposalID, input.Kind, input.TargetPath, input.BackupPath,
		boolInt(input.OriginalExists), input.OriginalSize, input.OriginalSHA256, input.OriginalJSON, now, now)
	if err != nil {
		return fmt.Errorf("create writeback transaction: %w", err)
	}
	return nil
}

func validateWriteback(input WritebackTransaction) error {
	if len(input.TransactionID) < 16 || len(input.TransactionID) > 128 || input.ProposalID <= 0 {
		return fmt.Errorf("invalid writeback identity")
	}
	if input.Kind != "tags" && input.Kind != "cover" && input.Kind != "nfo" {
		return fmt.Errorf("invalid writeback kind")
	}
	if strings.TrimSpace(input.TargetPath) == "" || len(input.TargetPath) > 16<<10 || len(input.BackupPath) > 16<<10 || input.OriginalSize < 0 {
		return fmt.Errorf("invalid writeback path or size")
	}
	if input.OriginalExists && (input.BackupPath == "" || len(input.OriginalSHA256) != 64) {
		return fmt.Errorf("existing target requires a verified backup")
	}
	if len(input.OriginalJSON) > 1<<20 {
		return fmt.Errorf("writeback snapshot too large")
	}
	return nil
}

func (r *WritebackRepository) UpdateState(ctx context.Context, transactionID, state string, cause error) error {
	if !validWritebackState(state) || state == "committed" {
		return fmt.Errorf("invalid direct writeback state %q", state)
	}
	message := ""
	if cause != nil {
		message = strings.TrimSpace(cause.Error())
		if len(message) > MaxWritebackErrorBytes {
			message = message[:MaxWritebackErrorBytes]
		}
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE writeback_transactions SET state=?,last_error=?,updated_at=? WHERE transaction_id=?`,
		state, message, time.Now().UTC().Unix(), transactionID)
	if err != nil {
		return fmt.Errorf("update writeback state: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *WritebackRepository) Commit(ctx context.Context, transactionID string, proposalID int64, originalJSON string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE writeback_transactions SET state='committed',last_error='',updated_at=?
		WHERE transaction_id=? AND proposal_id=? AND state='verified'`, time.Now().UTC().Unix(), transactionID, proposalID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("writeback transaction is not verified")
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE staging_changes SET status='applied',original_tags_json=?,applied_at=?
		WHERE id=? AND status='pending'`, originalJSON, time.Now().UTC().Unix(), proposalID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("staging proposal is not pending")
	}
	return tx.Commit()
}

func (r *WritebackRepository) MarkReconciled(ctx context.Context, transactionID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE writeback_transactions SET reconciled=1,updated_at=? WHERE transaction_id=? AND state='committed'`, time.Now().UTC().Unix(), transactionID)
	return err
}

func (r *WritebackRepository) CompleteUndo(ctx context.Context, transactionID string, proposalID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE writeback_transactions SET state='rolled_back',reconciled=1,last_error='',updated_at=?
		WHERE transaction_id=? AND proposal_id=? AND state='undoing'`, time.Now().UTC().Unix(), transactionID, proposalID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("writeback transaction is not committed")
	}
	result, err = tx.ExecContext(ctx, `UPDATE staging_changes SET status='reverted' WHERE id=? AND status='applied'`, proposalID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("staging proposal is not applied")
	}
	return tx.Commit()
}

func (r *WritebackRepository) BeginUndo(ctx context.Context, transactionID string, proposalID int64) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE writeback_transactions SET state='undoing',reconciled=0,updated_at=?
		WHERE transaction_id=? AND proposal_id=? AND state='committed'`, time.Now().UTC().Unix(), transactionID, proposalID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("writeback transaction is not committed")
	}
	return nil
}

func (r *WritebackRepository) GetByProposal(ctx context.Context, proposalID int64) (WritebackTransaction, error) {
	return r.getOne(ctx, `proposal_id=?`, proposalID)
}

func (r *WritebackRepository) ListIncomplete(ctx context.Context) ([]WritebackTransaction, error) {
	return r.list(ctx, `state IN ('prepared','written','verified','undoing','failed')`)
}

func (r *WritebackRepository) ListCommittedUnreconciled(ctx context.Context) ([]WritebackTransaction, error) {
	return r.list(ctx, `state='committed' AND reconciled=0`)
}

func (r *WritebackRepository) getOne(ctx context.Context, where string, arg any) (WritebackTransaction, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT transaction_id,proposal_id,kind,target_path,backup_path,original_exists,original_size,
		       original_sha256,original_json,state,reconciled,last_error,created_at,updated_at
		FROM writeback_transactions WHERE `+where, arg)
	return scanWriteback(row)
}

func (r *WritebackRepository) list(ctx context.Context, where string) ([]WritebackTransaction, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT transaction_id,proposal_id,kind,target_path,backup_path,original_exists,original_size,
		       original_sha256,original_json,state,reconciled,last_error,created_at,updated_at
		FROM writeback_transactions WHERE `+where+` ORDER BY created_at,transaction_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []WritebackTransaction
	for rows.Next() {
		value, err := scanWriteback(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type writebackScanner interface{ Scan(...any) error }

func scanWriteback(scanner writebackScanner) (WritebackTransaction, error) {
	var value WritebackTransaction
	var exists, reconciled int
	err := scanner.Scan(
		&value.TransactionID, &value.ProposalID, &value.Kind, &value.TargetPath, &value.BackupPath,
		&exists, &value.OriginalSize, &value.OriginalSHA256, &value.OriginalJSON, &value.State,
		&reconciled, &value.LastError, &value.CreatedAt, &value.UpdatedAt)
	value.OriginalExists = exists == 1
	value.Reconciled = reconciled == 1
	return value, err
}

func validWritebackState(state string) bool {
	switch state {
	case "prepared", "written", "verified", "committed", "undoing", "rolled_back", "failed":
		return true
	default:
		return false
	}
}

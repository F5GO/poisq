package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/F5GO/poisq/internal/musicmeta"
)

type MetadataPolicyRepository struct{ db *sql.DB }

func NewMetadataPolicyRepository(database *sql.DB) *MetadataPolicyRepository {
	return &MetadataPolicyRepository{db: database}
}

func (r *MetadataPolicyRepository) Get(ctx context.Context) (musicmeta.MergePolicy, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT source,provider,priority,refresh_mode,enabled,min_confidence
		FROM metadata_provider_policy ORDER BY priority,source,provider`)
	if err != nil {
		return musicmeta.MergePolicy{}, fmt.Errorf("read metadata provider policy: %w", err)
	}
	defer rows.Close()
	policy := musicmeta.MergePolicy{}
	for rows.Next() {
		var rule musicmeta.ProviderRule
		var enabled int
		if err := rows.Scan(&rule.Source, &rule.Provider, &rule.Priority, &rule.RefreshMode, &enabled, &rule.MinConfidence); err != nil {
			return musicmeta.MergePolicy{}, fmt.Errorf("scan metadata provider policy: %w", err)
		}
		rule.Enabled = enabled == 1
		policy.Rules = append(policy.Rules, rule)
	}
	if err := rows.Err(); err != nil {
		return musicmeta.MergePolicy{}, err
	}
	if len(policy.Rules) == 0 {
		return musicmeta.DefaultMergePolicy(), nil
	}
	return musicmeta.ValidateMergePolicy(policy)
}

func (r *MetadataPolicyRepository) Replace(ctx context.Context, input musicmeta.MergePolicy) error {
	policy, err := musicmeta.ValidateMergePolicy(input)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin metadata policy replacement: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM metadata_provider_policy`); err != nil {
		return fmt.Errorf("clear metadata provider policy: %w", err)
	}
	now := time.Now().UTC().Unix()
	for _, rule := range policy.Rules {
		enabled := 0
		if rule.Enabled {
			enabled = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO metadata_provider_policy(source,provider,priority,refresh_mode,enabled,min_confidence,updated_at)
			VALUES(?,?,?,?,?,?,?)`, rule.Source, rule.Provider, rule.Priority, rule.RefreshMode, enabled, rule.MinConfidence, now); err != nil {
			return fmt.Errorf("insert metadata provider rule: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit metadata policy replacement: %w", err)
	}
	return nil
}

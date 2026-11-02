// Package db отвечает за безопасное взаимодействие с базами данных SQLite (navidrome.db и poisq.db).
//
// Данный файл repo_staging.go реализует хранение и жизненный цикл предложений по рефакторингу
// метаданных и обложек (Staging-режим), включая сохранение слепка исходных тегов (original_tags_json)
// для мгновенного отката изменений (Undo).
package db

import (
	"database/sql"
	"time"
)

// StagingChangeRecord описывает одно предложение по исправлению тегов, кодировки или обложки.
type StagingChangeRecord struct {
	ID               int64  `json:"id"`
	Scope            string `json:"scope"`            // "track" или "folder"
	TargetID         string `json:"targetId"`         // ID трека или путь папки
	FolderPath       string `json:"folderPath"`       // Путь к папке альбома
	ChangeType       string `json:"changeType"`       // "mojibake", "musicbrainz", "cover", "artist_canonical"
	Confidence       int    `json:"confidence"`       // 0..100
	CurrentJSON      string `json:"currentJson"`      // Текущие метаданные
	ProposedJSON     string `json:"proposedJson"`     // Предлагаемые метаданные
	OriginalTagsJSON string `json:"originalTagsJson"` // Слепок тегов до применения (для Undo)
	Status           string `json:"status"`           // "pending", "applied", "ignored", "reverted"
	CreatedAt        int64  `json:"createdAt"`
	AppliedAt        int64  `json:"appliedAt"`
}

// StagingRepository инкапсулирует операции с таблицей staging_changes.
type StagingRepository struct {
	db *sql.DB
}

// NewStagingRepository создает новый экземпляр репозитория Staging-предложений.
func NewStagingRepository(db *sql.DB) *StagingRepository {
	return &StagingRepository{db: db}
}

// CreateProposal сохраняет новое предложение в статусе pending, если аналогичное активное предложение еще не создано.
func (r *StagingRepository) CreateProposal(rec StagingChangeRecord) (int64, error) {
	var existingID int64
	err := r.db.QueryRow(`
		SELECT id FROM staging_changes
		WHERE target_id = ? AND change_type = ? AND status = 'pending'
		LIMIT 1
	`, rec.TargetID, rec.ChangeType).Scan(&existingID)
	if err == nil && existingID > 0 {
		_, err = r.db.Exec(`
			UPDATE staging_changes
			SET confidence = ?, current_json = ?, proposed_json = ?
			WHERE id = ?
		`, rec.Confidence, rec.CurrentJSON, rec.ProposedJSON, existingID)
		return existingID, err
	}

	now := time.Now().Unix()
	if rec.Scope == "" {
		rec.Scope = "track"
	}
	res, err := r.db.Exec(`
		INSERT INTO staging_changes (
			scope, target_id, folder_path, change_type, confidence,
			current_json, proposed_json, original_tags_json, status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, '{}', 'pending', ?)
	`, rec.Scope, rec.TargetID, rec.FolderPath, rec.ChangeType, rec.Confidence, rec.CurrentJSON, rec.ProposedJSON, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetByID возвращает запись Staging-предложения по его идентификатору.
func (r *StagingRepository) GetByID(id int64) (*StagingChangeRecord, error) {
	var s StagingChangeRecord
	err := r.db.QueryRow(`
		SELECT id, scope, target_id, folder_path, change_type, confidence,
		       current_json, proposed_json, original_tags_json, status, created_at, applied_at
		FROM staging_changes WHERE id = ?
	`, id).Scan(
		&s.ID, &s.Scope, &s.TargetID, &s.FolderPath, &s.ChangeType, &s.Confidence,
		&s.CurrentJSON, &s.ProposedJSON, &s.OriginalTagsJSON, &s.Status, &s.CreatedAt, &s.AppliedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// List возвращает список предложений с фильтрацией по статусу (pending, applied, ignored, reverted).
func (r *StagingRepository) List(statusFilter string, limit int) ([]StagingChangeRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	query := `
		SELECT id, scope, target_id, folder_path, change_type, confidence,
		       current_json, proposed_json, original_tags_json, status, created_at, applied_at
		FROM staging_changes
	`
	var args []any
	if statusFilter != "" && statusFilter != "all" {
		query += ` WHERE status = ?`
		args = append(args, statusFilter)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []StagingChangeRecord
	for rows.Next() {
		var s StagingChangeRecord
		if err := rows.Scan(
			&s.ID, &s.Scope, &s.TargetID, &s.FolderPath, &s.ChangeType, &s.Confidence,
			&s.CurrentJSON, &s.ProposedJSON, &s.OriginalTagsJSON, &s.Status, &s.CreatedAt, &s.AppliedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// MarkApplied сохраняет резервную копию исходных тегов (original_tags_json) и переводит предложение в статус applied.
func (r *StagingRepository) MarkApplied(id int64, originalTagsJSON string) error {
	_, err := r.db.Exec(`
		UPDATE staging_changes
		SET status = 'applied', original_tags_json = ?, applied_at = ?
		WHERE id = ?
	`, originalTagsJSON, time.Now().Unix(), id)
	return err
}

// MarkStatus обновляет статус предложения (например, 'ignored' или 'reverted' после Undo).
func (r *StagingRepository) MarkStatus(id int64, status string) error {
	_, err := r.db.Exec(`UPDATE staging_changes SET status = ? WHERE id = ?`, status, id)
	return err
}

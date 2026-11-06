// Package db отвечает за безопасное взаимодействие с базами данных SQLite (navidrome.db и poisq.db).
//
// Данный файл repo_confidence.go хранит уверенность акустического выравнивания по строкам
// (таблица lrc_confidence) и ручные правки таймингов из редактора Studio (таблица lrc_corrections).
// Уверенность нужна, чтобы подсвечивать в редакторе строки, времени которых нельзя верить, а правки
// «было → стало» накапливаются как реальный эталон для проверки и настройки алгоритма.
package db

import (
	"database/sql"
	"time"
)

// ConfidenceRecord — сохранённая уверенность по строкам трека.
type ConfidenceRecord struct {
	TrackID     string `json:"trackId"`
	LinesJSON   string `json:"linesJson"`   // [{"text","sec","level","reason"}]
	SummaryJSON string `json:"summaryJson"` // счётчики high/medium/low
	UpdatedAt   int64  `json:"updatedAt"`
}

// Correction — одна ручная правка времени строки.
type Correction struct {
	LineText     string  `json:"text"`
	AutoSec      float64 `json:"from"`
	CorrectedSec float64 `json:"to"`
	Confidence   string  `json:"confidence"`
}

// ConfidenceRepository — хранилище уверенности и правок.
type ConfidenceRepository struct {
	db *sql.DB
}

// NewConfidenceRepository создаёт репозиторий.
func NewConfidenceRepository(db *sql.DB) *ConfidenceRepository {
	return &ConfidenceRepository{db: db}
}

// Set сохраняет или заменяет уверенность трека.
func (r *ConfidenceRepository) Set(trackID, linesJSON, summaryJSON string) error {
	if linesJSON == "" {
		linesJSON = "[]"
	}
	if summaryJSON == "" {
		summaryJSON = "{}"
	}
	_, err := r.db.Exec(`
		INSERT INTO lrc_confidence (track_id, lines_json, summary_json, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(track_id) DO UPDATE SET lines_json = excluded.lines_json,
			summary_json = excluded.summary_json, updated_at = excluded.updated_at`,
		trackID, linesJSON, summaryJSON, time.Now().Unix())
	return err
}

// Get возвращает уверенность трека; (nil, nil), если её нет.
func (r *ConfidenceRepository) Get(trackID string) (*ConfidenceRecord, error) {
	var rec ConfidenceRecord
	err := r.db.QueryRow(`SELECT track_id, lines_json, summary_json, updated_at FROM lrc_confidence WHERE track_id = ?`, trackID).
		Scan(&rec.TrackID, &rec.LinesJSON, &rec.SummaryJSON, &rec.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// Delete удаляет уверенность трека (например, при удалении текста).
func (r *ConfidenceRepository) Delete(trackID string) error {
	_, err := r.db.Exec(`DELETE FROM lrc_confidence WHERE track_id = ?`, trackID)
	return err
}

// AddCorrections записывает ручные правки одной транзакцией.
func (r *ConfidenceRepository) AddCorrections(trackID string, items []Correction) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, c := range items {
		if _, err := tx.Exec(`INSERT INTO lrc_corrections (track_id, line_text, auto_sec, corrected_sec, confidence, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, trackID, c.LineText, c.AutoSec, c.CorrectedSec, c.Confidence, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CountCorrections возвращает число сохранённых правок (по треку или всего, если trackID пуст).
func (r *ConfidenceRepository) CountCorrections(trackID string) (int, error) {
	var n int
	var err error
	if trackID == "" {
		err = r.db.QueryRow(`SELECT COUNT(*) FROM lrc_corrections`).Scan(&n)
	} else {
		err = r.db.QueryRow(`SELECT COUNT(*) FROM lrc_corrections WHERE track_id = ?`, trackID).Scan(&n)
	}
	return n, err
}

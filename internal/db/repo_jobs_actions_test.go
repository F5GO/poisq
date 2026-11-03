// Package db отвечает за безопасное взаимодействие с базами данных SQLite (navidrome.db и poisq.db).
//
// Что делает этот код и для чего он нужен:
// Данный файл repo_jobs_actions_test.go содержит модульные тесты для индивидуального управления
// фоновыми задачами воркера: постановка задачи на паузу (PauseJob), возобновление (ResumeJob),
// повторный перезапуск (RetryJob) и удаление (DeleteJob).
package db

import (
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestJobActions(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_jobs.db")

	db, err := OpenPoisqDatabase(dbPath)
	if err != nil {
		t.Fatalf("не удалось открыть тестовую БД: %v", err)
	}
	defer db.Close()

	repo := NewJobRepository(db)

	// Ставим задачу в очередь
	if err := repo.Enqueue("track_1", "tournament", 5); err != nil {
		t.Fatalf("ошибка Enqueue: %v", err)
	}

	jobs, err := repo.ListRecent(10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("ожидалась 1 задача, получено: %d (err: %v)", len(jobs), err)
	}
	jobID := jobs[0].ID

	// 1. Тест PauseJob
	if err := repo.PauseJob(jobID); err != nil {
		t.Fatalf("ошибка PauseJob: %v", err)
	}
	jobs, _ = repo.ListRecent(10)
	if jobs[0].Status != "paused" {
		t.Errorf("ожидался статус 'paused', получен: %s", jobs[0].Status)
	}

	// 2. Тест ResumeJob
	if err := repo.ResumeJob(jobID); err != nil {
		t.Fatalf("ошибка ResumeJob: %v", err)
	}
	jobs, _ = repo.ListRecent(10)
	if jobs[0].Status != "pending" || jobs[0].NextRetryAt != 0 {
		t.Errorf("ожидался статус 'pending' с next_retry_at=0, получено: %s, %d", jobs[0].Status, jobs[0].NextRetryAt)
	}

	// Симулируем ошибку и попытку
	_ = repo.MarkRetryOrFailed(jobID, 2, 3, "ошибка сети")
	jobs, _ = repo.ListRecent(10)
	if jobs[0].ErrorLog != "ошибка сети" {
		t.Errorf("ожидался errorLog 'ошибка сети', получен: %s", jobs[0].ErrorLog)
	}

	// 3. Тест RetryJob
	if err := repo.RetryJob(jobID); err != nil {
		t.Fatalf("ошибка RetryJob: %v", err)
	}
	jobs, _ = repo.ListRecent(10)
	if jobs[0].Status != "pending" || jobs[0].AttemptCount != 0 || jobs[0].ErrorLog != "" {
		t.Errorf("после RetryJob ожидался статус 'pending', attempts=0, errorLog='', получено: %s, %d, %s",
			jobs[0].Status, jobs[0].AttemptCount, jobs[0].ErrorLog)
	}

	// 4. Тест DeleteJob
	if err := repo.DeleteJob(jobID); err != nil {
		t.Fatalf("ошибка DeleteJob: %v", err)
	}
	jobs, _ = repo.ListRecent(10)
	if len(jobs) != 0 {
		t.Errorf("после DeleteJob ожидалось 0 задач, получено: %d", len(jobs))
	}
}

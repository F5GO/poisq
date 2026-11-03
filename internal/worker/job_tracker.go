// Package worker реализует фоновый обработчик персистентной очереди задач (lyrics_jobs)
// с автоматической постановкой на паузу при исчерпании квот API-ключей и атомарной
// записью готовых .lrc файлов рядом с треками в папку /music.
//
// Что делает этот код и для чего он нужен:
// Данный файл job_tracker.go реализует потокобезопасный трекер живой телеметрии и пошагового
// журнала выполнения фоновых задач. Позволяет пользователю в веб-интерфейсе при клике на задачу
// видеть в реальном времени каждый шаг под капотом (турнир, сжатие Opus, Groq Whisper, Gemini Merge).
package worker

import (
	"fmt"
	"sync"
	"time"
)

// JobStepEntry представляет одну запись в журнале выполнения задачи.
type JobStepEntry struct {
	Timestamp int64  `json:"timestamp"`
	TimeStr   string `json:"timeStr"`
	Stage     string `json:"stage"`
	Message   string `json:"message"`
}

// JobTelemetryTracker хранит живые логи и прогресс выполнения задач в памяти.
type JobTelemetryTracker struct {
	mu      sync.RWMutex
	entries map[int64][]JobStepEntry
	order   []int64
	maxJobs int
}

// NewJobTelemetryTracker создает новый трекер телеметрии задач.
func NewJobTelemetryTracker() *JobTelemetryTracker {
	return &JobTelemetryTracker{
		entries: make(map[int64][]JobStepEntry),
		maxJobs: 200,
	}
}

// RecordStep добавляет новый шаг в журнал задачи.
func (t *JobTelemetryTracker) RecordStep(jobID int64, stage string, message string) {
	now := time.Now()
	entry := JobStepEntry{
		Timestamp: now.Unix(),
		TimeStr:   now.Format("15:04:05"),
		Stage:     stage,
		Message:   message,
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if _, exists := t.entries[jobID]; !exists {
		t.order = append(t.order, jobID)
		if len(t.order) > t.maxJobs {
			oldest := t.order[0]
			t.order = t.order[1:]
			delete(t.entries, oldest)
		}
	}

	t.entries[jobID] = append(t.entries[jobID], entry)
}

// ClearJob очищает старый лог задачи перед её полным перезапуском.
func (t *JobTelemetryTracker) ClearJob(jobID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, jobID)
}

// GetSteps возвращает список всех записанных шагов для указанной задачи.
func (t *JobTelemetryTracker) GetSteps(jobID int64) []JobStepEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()

	list := t.entries[jobID]
	if len(list) == 0 {
		return nil
	}
	cp := make([]JobStepEntry, len(list))
	copy(cp, list)
	return cp
}

// FormatLogLines возвращает шаги задачи в виде готовых строк для терминала в UI.
func (t *JobTelemetryTracker) FormatLogLines(jobID int64) []string {
	steps := t.GetSteps(jobID)
	if len(steps) == 0 {
		return nil
	}
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, fmt.Sprintf("[%s] %s", s.TimeStr, s.Message))
	}
	return out
}

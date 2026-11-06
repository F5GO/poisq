// Package aligner — клиент сервиса акустического выравнивания (poisq-aligner, каталог aligner/).
//
// Сервис по аудиофайлу и тексту песни возвращает время каждой строки с оценкой уверенности и готовый .lrc.
// Клиент отправляет задачу, опрашивает статус, передаёт в журнал воркера этапы выполнения и отменяет
// задачу при остановке. Недоступность сервиса отличается от ошибки выравнивания (ErrUnavailable), чтобы
// воркер мог откатиться на облачный режим.
package aligner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnavailable — сервис выравнивания не отвечает (не запущен, сеть, 502/503).
var ErrUnavailable = errors.New("сервис выравнивания недоступен")

// ErrJobLost — сервис не знает такую задачу (например, был перезапущен во время выполнения).
var ErrJobLost = errors.New("задача потеряна на стороне сервиса выравнивания")

// JobError — ошибка выполнения задачи с кодом: quota, invalid_key, cpu_unsupported, bad_request, timeout, disk, failed.
type JobError struct {
	Code    string
	Message string
}

func (e *JobError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// SubmitRequest — тело запроса постановки задачи.
type SubmitRequest struct {
	AudioPath string   `json:"audio_path"`
	Lines     []string `json:"lines"`
	Lang      string   `json:"lang,omitempty"`
	TrackID   string   `json:"track_id,omitempty"`
	GroqKey   string   `json:"groq_key"`
	Fresh     bool     `json:"fresh,omitempty"`
}

// Line — результат по одной строке.
type Line struct {
	Index      int     `json:"index"`
	Text       string  `json:"text"`
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	LRCTime    float64 `json:"lrc_time"`
	Confidence string  `json:"confidence"` // high | medium | low
	Reason     string  `json:"reason"`
	Estimated  bool    `json:"estimated"`
}

// Summary — сводка по треку.
type Summary struct {
	Lines     int     `json:"lines"`
	High      int     `json:"high"`
	Medium    int     `json:"medium"`
	Low       int     `json:"low"`
	Estimated int     `json:"estimated"`
	LowRatio  float64 `json:"low_ratio"`
	Seconds   float64 `json:"seconds"`
}

// Result — итог выравнивания.
type Result struct {
	Lines   []Line  `json:"lines"`
	LRC     string  `json:"lrc"`
	Summary Summary `json:"summary"`
}

// ProgressEntry — запись журнала этапов.
type ProgressEntry struct {
	T   float64 `json:"t"`
	Msg string  `json:"msg"`
}

// JobStatus — состояние задачи.
type JobStatus struct {
	ID        string          `json:"id"`
	TrackID   string          `json:"track_id"`
	Status    string          `json:"status"` // queued | running | done | error | cancelled
	Progress  []ProgressEntry `json:"progress"`
	Error     string          `json:"error"`
	ErrorCode string          `json:"error_code"`
	Result    *Result         `json:"result,omitempty"`
}

// Client — клиент HTTP API сервиса.
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// NewClient создаёт клиента. token может быть пустым (авторизация выключена).
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
		hc:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout {
		return resp.StatusCode, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("некорректный ответ сервиса выравнивания: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// Healthy проверяет доступность сервиса.
func (c *Client) Healthy(ctx context.Context) error {
	var out struct {
		OK bool `json:"ok"`
	}
	code, err := c.do(ctx, http.MethodGet, "/healthz", nil, &out)
	if err != nil {
		return err
	}
	if code != http.StatusOK || !out.OK {
		return fmt.Errorf("%w: healthz вернул HTTP %d", ErrUnavailable, code)
	}
	return nil
}

// Submit ставит задачу в очередь сервиса и возвращает её идентификатор.
func (c *Client) Submit(ctx context.Context, req SubmitRequest) (string, error) {
	var out struct {
		ID        string `json:"id"`
		Error     string `json:"error"`
		ErrorCode string `json:"error_code"`
	}
	code, err := c.do(ctx, http.MethodPost, "/v1/jobs", req, &out)
	if err != nil {
		return "", err
	}
	if code != http.StatusAccepted {
		return "", &JobError{Code: firstNonEmpty(out.ErrorCode, "failed"), Message: firstNonEmpty(out.Error, fmt.Sprintf("HTTP %d", code))}
	}
	return out.ID, nil
}

// Job возвращает состояние задачи; ErrJobLost, если сервис её не знает.
func (c *Client) Job(ctx context.Context, id string) (*JobStatus, error) {
	var st JobStatus
	code, err := c.do(ctx, http.MethodGet, "/v1/jobs/"+id, nil, &st)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return nil, ErrJobLost
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("сервис выравнивания вернул HTTP %d", code)
	}
	return &st, nil
}

// Cancel отменяет задачу (ошибки не критичны).
func (c *Client) Cancel(ctx context.Context, id string) {
	_, _ = c.do(ctx, http.MethodDelete, "/v1/jobs/"+id, nil, nil)
}

// Run ставит задачу, опрашивает её до завершения и возвращает результат. onProgress получает новые записи
// журнала. При отмене контекста задача отменяется и на стороне сервиса.
func (c *Client) Run(ctx context.Context, req SubmitRequest, pollEvery time.Duration, onProgress func(string)) (*Result, error) {
	if pollEvery <= 0 {
		pollEvery = 5 * time.Second
	}
	id, err := c.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	seen := 0
	failures := 0
	for {
		select {
		case <-ctx.Done():
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			c.Cancel(cctx, id)
			cancel()
			return nil, ctx.Err()
		case <-time.After(pollEvery):
		}
		st, err := c.Job(ctx, id)
		if err != nil {
			if errors.Is(err, ErrJobLost) {
				return nil, err
			}
			// короткие сбои опроса терпим: конвейер работает независимо от соединения
			failures++
			if failures >= 12 {
				return nil, err
			}
			continue
		}
		failures = 0
		if onProgress != nil {
			for ; seen < len(st.Progress); seen++ {
				onProgress(st.Progress[seen].Msg)
			}
		}
		switch st.Status {
		case "done":
			if st.Result == nil {
				return nil, &JobError{Code: "failed", Message: "сервис вернул пустой результат"}
			}
			return st.Result, nil
		case "error":
			return nil, &JobError{Code: firstNonEmpty(st.ErrorCode, "failed"), Message: st.Error}
		case "cancelled":
			return nil, &JobError{Code: "cancelled", Message: "задача отменена"}
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

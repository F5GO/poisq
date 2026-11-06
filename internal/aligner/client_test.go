package aligner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "tok"), srv
}

func TestHealthyAndAuth(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		w.WriteHeader(404)
	})
	if err := c.Healthy(context.Background()); err != nil {
		t.Fatalf("Healthy: %v", err)
	}
}

func TestUnavailableWhenServerDown(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "")
	err := c.Healthy(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ожидалась ErrUnavailable, получено %v", err)
	}
}

func TestSubmitSendsTokenAndBody(t *testing.T) {
	var gotAuth string
	var got SubmitRequest
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"abc","status":"queued"}`))
	})
	id, err := c.Submit(context.Background(), SubmitRequest{AudioPath: "/music/a.flac", Lines: []string{"x"}, GroqKey: "k", TrackID: "t"})
	if err != nil || id != "abc" {
		t.Fatalf("Submit: %v %q", err, id)
	}
	if gotAuth != "Bearer tok" || got.AudioPath != "/music/a.flac" || got.GroqKey != "k" {
		t.Fatalf("неверный запрос: %q %+v", gotAuth, got)
	}
}

func TestSubmitRejectedMapsToJobError(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"audio_path вне каталога медиатеки","error_code":"bad_request"}`))
	})
	_, err := c.Submit(context.Background(), SubmitRequest{})
	var je *JobError
	if !errors.As(err, &je) || je.Code != "bad_request" {
		t.Fatalf("ожидалась JobError bad_request, получено %v", err)
	}
}

func TestRunPollsAndReportsProgress(t *testing.T) {
	var polls int32
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"j1","status":"queued"}`))
		case r.Method == http.MethodGet:
			n := atomic.AddInt32(&polls, 1)
			switch {
			case n == 1:
				_, _ = w.Write([]byte(`{"id":"j1","status":"running","progress":[{"t":1,"msg":"этап 1"}]}`))
			default:
				_, _ = w.Write([]byte(`{"id":"j1","status":"done","progress":[{"t":1,"msg":"этап 1"},{"t":2,"msg":"этап 2"}],
					"result":{"lrc":"[00:01.00]a","lines":[{"index":0,"text":"a","start":1.5,"end":2,"lrc_time":1,"confidence":"high"}],
					"summary":{"lines":1,"high":1,"low":0,"low_ratio":0}}}`))
			}
		}
	})
	var msgs []string
	res, err := c.Run(context.Background(), SubmitRequest{}, 5*time.Millisecond, func(m string) { msgs = append(msgs, m) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LRC != "[00:01.00]a" || len(res.Lines) != 1 || res.Lines[0].Confidence != "high" {
		t.Fatalf("неверный результат: %+v", res)
	}
	if len(msgs) != 2 || msgs[0] != "этап 1" || msgs[1] != "этап 2" {
		t.Fatalf("прогресс должен приходить один раз по каждому этапу: %v", msgs)
	}
}

func TestRunMapsErrorStatus(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"j1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"j1","status":"error","error":"Groq HTTP 429","error_code":"quota"}`))
	})
	_, err := c.Run(context.Background(), SubmitRequest{}, time.Millisecond, nil)
	var je *JobError
	if !errors.As(err, &je) || je.Code != "quota" {
		t.Fatalf("ожидалась JobError quota, получено %v", err)
	}
}

func TestRunDetectsLostJob(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"j1"}`))
			return
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"job not found"}`))
	})
	_, err := c.Run(context.Background(), SubmitRequest{}, time.Millisecond, nil)
	if !errors.Is(err, ErrJobLost) {
		t.Fatalf("ожидалась ErrJobLost, получено %v", err)
	}
}

func TestRunCancelsRemoteJobOnContextDone(t *testing.T) {
	var cancelled int32
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"j1"}`))
		case http.MethodDelete:
			atomic.StoreInt32(&cancelled, 1)
			_, _ = w.Write([]byte(`{"id":"j1","status":"cancelled"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"j1","status":"running"}`))
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err := c.Run(ctx, SubmitRequest{}, 10*time.Millisecond, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ожидался DeadlineExceeded, получено %v", err)
	}
	if atomic.LoadInt32(&cancelled) != 1 {
		t.Fatal("задача на стороне сервиса должна быть отменена")
	}
}

func TestRunToleratesTransientPollFailures(t *testing.T) {
	var n int32
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"j1"}`))
			return
		}
		if atomic.AddInt32(&n, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"id":"j1","status":"done","result":{"lrc":"x","lines":[],"summary":{}}}`))
	})
	if _, err := c.Run(context.Background(), SubmitRequest{}, time.Millisecond, nil); err != nil {
		t.Fatalf("короткие сбои опроса не должны ронять задачу: %v", err)
	}
}

// TestDecodesRealServiceResponse разбирает ответ, снятый с настоящего прогона сервиса (Python): защищает контракт
// (имена полей lrc_time, confidence, summary.low_ratio и т.д.) от расхождений между Go-клиентом и сервисом.
func TestDecodesRealServiceResponse(t *testing.T) {
	raw, err := os.ReadFile("testdata/job_done_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"9cecfefd64f7479b","status":"queued"}`))
			return
		}
		_, _ = w.Write(raw)
	})
	res, err := c.Run(context.Background(), SubmitRequest{}, time.Millisecond, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Summary.Lines != 60 || res.Summary.Low == 0 || res.Summary.LowRatio <= 0 || res.Summary.Seconds <= 0 {
		t.Fatalf("summary: %+v", res.Summary)
	}
	if len(res.Lines) != 5 || res.LRC == "" {
		t.Fatalf("lines/lrc: %d %q", len(res.Lines), res.LRC)
	}
	levels := map[string]bool{}
	for _, l := range res.Lines {
		levels[l.Confidence] = true
		if l.Text == "" || l.Start <= 0 || l.End <= l.Start || l.LRCTime < 0 || l.LRCTime > l.Start {
			t.Fatalf("неверная строка: %+v", l)
		}
	}
	if !levels["high"] || !levels["low"] {
		t.Fatalf("в образце должны быть строки high и low: %v", levels)
	}
}

package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestResilientClient() *ResilientClient {
	return NewResilientClient(2*time.Second, 4, 0, 1)
}

func TestResilientClientRestrictedOriginAllowsSameOriginRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	client := newTestResilientClient()
	if err := client.RestrictOrigin(server.URL); err != nil {
		t.Fatalf("restrict origin: %v", err)
	}
	body, status, _, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, server.URL+"/start", nil)
	})
	if err != nil || status != http.StatusOK || string(body) != "ok" {
		t.Fatalf("same-origin redirect: status=%d body=%q err=%v", status, body, err)
	}
}

func TestResilientClientRestrictedOriginRejectsCrossOriginWithoutLeak(t *testing.T) {
	const secret = "credential-marker"
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalls.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen?token="+secret, http.StatusFound)
	}))
	defer source.Close()
	client := newTestResilientClient()
	if err := client.RestrictOrigin(source.URL); err != nil {
		t.Fatalf("restrict origin: %v", err)
	}
	_, _, _, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		req, buildErr := http.NewRequest(http.MethodGet, source.URL+"/start", nil)
		if buildErr == nil {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		return req, buildErr
	})
	if err == nil {
		t.Fatal("cross-origin redirect accepted")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), target.URL) || strings.Contains(err.Error(), source.URL) {
		t.Fatalf("redirect error leaked request details: %v", err)
	}
	if targetCalls.Load() != 0 {
		t.Fatalf("redirect target was contacted: %d", targetCalls.Load())
	}
}

func TestResilientClientRestrictedOriginRejectsInitialForeignRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client := newTestResilientClient()
	if err := client.RestrictOrigin("https://allowed.example"); err != nil {
		t.Fatalf("restrict origin: %v", err)
	}
	_, _, _, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, server.URL, nil)
	})
	if err == nil {
		t.Fatal("foreign initial request accepted")
	}
	if calls.Load() != 0 {
		t.Fatalf("foreign origin was contacted: %d", calls.Load())
	}
}

func TestResilientClientErrorsDoNotLeakBodiesOrURLs(t *testing.T) {
	const secret = "provider-secret-body"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(secret))
	}))
	defer server.Close()
	client := newTestResilientClient()
	_, _, _, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, server.URL+"?token="+secret, nil)
	})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("unsafe provider error: %v", err)
	}
}

func TestResilientClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 128)))
	}))
	defer server.Close()
	client := newTestResilientClient()
	client.maxBytes = 64
	_, _, _, err := client.DoWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, server.URL, nil)
	})
	if err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestResilientClientPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := newTestResilientClient()
	_, _, _, err := client.DoWithRetry(ctx, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, "https://example.com", nil)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestResilientClientRejectsInvalidRestrictedOrigin(t *testing.T) {
	client := newTestResilientClient()
	for _, origin := range []string{"", "file:///tmp/data", "https://user:pass@example.com", "https://example.com/path"} {
		if err := client.RestrictOrigin(origin); err == nil {
			t.Fatalf("invalid origin accepted: %q", origin)
		}
	}
}

package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestCastEndpointsRequireNavidromeAuth(t *testing.T) {
	var pings atomic.Int32
	nav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/ping" {
			http.NotFound(w, r)
			return
		}
		pings.Add(1)
		q := r.URL.Query()
		if q.Get("u") == "alice" && q.Get("t") == "good" && q.Get("s") == "salt" {
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"subsonic-response":{"status":"failed","error":{"code":40}}}`))
	}))
	defer nav.Close()

	u, _ := url.Parse(nav.URL)
	srv := NewServer(u, nil, nil, nil, false)
	var reached atomic.Int32
	srv.SetCast(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))

	get := func(query string) {
		srv.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/rest/poisqCastZones?"+query, nil))
	}

	get("")                         // без авторизации — Navidrome даже не спрашиваем
	get("u=alice&t=bad&s=salt&c=x") // неверный токен
	if reached.Load() != 0 {
		t.Fatalf("unauthenticated request reached cast handler")
	}

	get("u=alice&t=good&s=salt&c=x")
	get("u=alice&t=good&s=salt&c=x") // второй раз — из кеша
	if reached.Load() != 2 {
		t.Fatalf("authenticated requests reached handler %d times, want 2", reached.Load())
	}
	if pings.Load() != 2 { // bad + первый good
		t.Fatalf("navidrome pinged %d times, want 2", pings.Load())
	}
}

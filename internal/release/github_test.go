package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGitHubLatestSuccess(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","published_at":"2026-05-23T10:00:00Z","html_url":"https://example/r"}`))
	}))
	defer server.Close()

	c := NewCache(server.URL, "owner/repo", time.Hour)
	c.client = server.Client()

	r1, err := c.Latest(context.Background())
	if err != nil || r1 == nil {
		t.Fatalf("first call: r=%v err=%v", r1, err)
	}
	if r1.Tag != "v1.2.3" {
		t.Errorf("Tag = %q, want v1.2.3", r1.Tag)
	}

	// Second call within TTL should hit cache.
	r2, err := c.Latest(context.Background())
	if err != nil || r2 == nil {
		t.Fatalf("second call: r=%v err=%v", r2, err)
	}
	if hits.Load() != 1 {
		t.Errorf("expected 1 server hit, got %d", hits.Load())
	}
}

func TestGitHubLatestFailureReturnsNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // rate-limited
	}))
	defer server.Close()

	c := NewCache(server.URL, "owner/repo", time.Hour)
	c.client = server.Client()

	r, err := c.Latest(context.Background())
	if err != nil {
		t.Errorf("expected nil error on 403, got %v", err)
	}
	if r != nil {
		t.Errorf("expected nil release on 403, got %+v", r)
	}
}

func TestGitHubLatestBadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("garbage"))
	}))
	defer server.Close()

	c := NewCache(server.URL, "owner/repo", time.Hour)
	c.client = server.Client()

	r, err := c.Latest(context.Background())
	if err != nil || r != nil {
		t.Errorf("bad JSON should return (nil,nil), got (%+v, %v)", r, err)
	}
}

func TestGitHubLatestTTLExpiry(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","published_at":"2026-05-23T10:00:00Z","html_url":"https://example/r"}`))
	}))
	defer server.Close()

	c := NewCache(server.URL, "owner/repo", 10*time.Millisecond)
	c.client = server.Client()

	if _, _ = c.Latest(context.Background()); hits.Load() != 1 {
		t.Fatalf("after first call hits=%d, want 1", hits.Load())
	}
	time.Sleep(20 * time.Millisecond)
	if _, _ = c.Latest(context.Background()); hits.Load() != 2 {
		t.Errorf("after TTL expiry hits=%d, want 2 (cache should refresh)", hits.Load())
	}
}

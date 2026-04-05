package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQuietRequestLogger(t *testing.T) {
	t.Run("skips healthy fast requests", func(t *testing.T) {
		var buf bytes.Buffer
		logger := log.New(&buf, "", 0)
		handler := newQuietRequestLogger(logger, time.Second)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/runner/status", nil)
		req.RemoteAddr = "127.0.0.1:24085"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := buf.String(); got != "" {
			t.Fatalf("expected no request log, got %q", got)
		}
	})

	t.Run("logs client errors", func(t *testing.T) {
		var buf bytes.Buffer
		logger := log.New(&buf, "", 0)
		handler := newQuietRequestLogger(logger, time.Second)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.NotFound(w, nil)
		}))

		req := httptest.NewRequest(http.MethodGet, "/missing", nil)
		req.RemoteAddr = "192.168.20.10:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		got := buf.String()
		if !strings.Contains(got, "api: GET /missing status=404") {
			t.Fatalf("expected 404 request log, got %q", got)
		}
	})

	t.Run("logs slow successful requests", func(t *testing.T) {
		var buf bytes.Buffer
		logger := log.New(&buf, "", 0)
		handler := newQuietRequestLogger(logger, time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(5 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/storage", nil)
		req.RemoteAddr = "[::1]:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		got := buf.String()
		if !strings.Contains(got, "api: GET /api/v1/storage status=200") {
			t.Fatalf("expected slow request log, got %q", got)
		}
		if !strings.Contains(got, "remote=::1") {
			t.Fatalf("expected remote host in log, got %q", got)
		}
	})
}

func TestReadOnlyGuard(t *testing.T) {
	t.Parallel()

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := ReadOnlyGuard(okHandler)

	tests := []struct {
		name       string
		method     string
		wantStatus int
	}{
		{"GET passes through", http.MethodGet, http.StatusOK},
		{"HEAD passes through", http.MethodHead, http.StatusOK},
		{"POST blocked", http.MethodPost, http.StatusForbidden},
		{"PUT blocked", http.MethodPut, http.StatusForbidden},
		{"DELETE blocked", http.MethodDelete, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tt.method, "/", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

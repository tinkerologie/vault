package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
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

func TestAPIKeyAuth(t *testing.T) {
	t.Parallel()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	middleware := APIKeyAuth(database)
	handler := middleware(okHandler)

	apiKey := "vault_testkey_abc123"
	hash, err := crypto.HashPassphrase(apiKey)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		remoteAddr string
		header     string
		setupKey   bool
		wantStatus int
	}{
		{
			name:       "loopback exempt ipv4",
			remoteAddr: "127.0.0.1:12345",
			setupKey:   true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "loopback exempt ipv6",
			remoteAddr: "[::1]:12345",
			setupKey:   true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "no key configured passes through",
			remoteAddr: "192.168.1.50:12345",
			setupKey:   false,
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid key from remote",
			remoteAddr: "192.168.1.50:12345",
			header:     apiKey,
			setupKey:   true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid key from remote",
			remoteAddr: "192.168.1.50:12345",
			header:     "vault_wrong_key",
			setupKey:   true,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing key from remote",
			remoteAddr: "192.168.1.50:12345",
			header:     "",
			setupKey:   true,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset api_key_hash setting for each subtest.
			if err := database.SetSetting("api_key_hash", ""); err != nil {
				t.Fatalf("resetting api_key_hash: %v", err)
			}
			if tt.setupKey {
				if err := database.SetSetting("api_key_hash", hash); err != nil {
					t.Fatalf("setting api_key_hash: %v", err)
				}
			}

			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.header != "" {
				req.Header.Set("X-API-Key", tt.header)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

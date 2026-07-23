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

	"github.com/go-chi/cors"

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

func TestPrivateNetworkAccess(t *testing.T) {
	t.Parallel()

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := PrivateNetworkAccess(okHandler)

	tests := []struct {
		name      string
		method    string
		pnaHeader string
		wantAllow bool
	}{
		{"preflight with PNA request gets allow header", http.MethodOptions, "true", true},
		{"preflight without PNA request header is untouched", http.MethodOptions, "", false},
		{"non-preflight request is untouched", http.MethodGet, "true", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tt.method, "/api/v1/health", nil)
			if tt.pnaHeader != "" {
				req.Header.Set("Access-Control-Request-Private-Network", tt.pnaHeader)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			got := w.Header().Get("Access-Control-Allow-Private-Network")
			if tt.wantAllow && got != "true" {
				t.Fatalf("expected Access-Control-Allow-Private-Network=true, got %q", got)
			}
			if !tt.wantAllow && got != "" {
				t.Fatalf("expected no Access-Control-Allow-Private-Network header, got %q", got)
			}
		})
	}
}

// TestPrivateNetworkAccessWithCORS composes PrivateNetworkAccess with the real
// cors.Handler in the same order the router wires them (PNA outer, CORS inner),
// and asserts a realistic PNA preflight receives BOTH the standard CORS headers
// AND Access-Control-Allow-Private-Network — the browser rejects the preflight
// unless all are present (issue #250).
func TestPrivateNetworkAccessWithCORS(t *testing.T) {
	t.Parallel()

	corsMW := cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*.myunraid.net", "http://localhost:*", "http://127.0.0.1:*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "X-API-Key"},
		AllowCredentials: true,
		MaxAge:           300,
	})
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := PrivateNetworkAccess(corsMW(final))

	// Realistic PNA preflight: allowed origin + request-method + PNA request header.
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/health", nil)
	req.Header.Set("Origin", "https://tower.myunraid.net")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://tower.myunraid.net" {
		t.Fatalf("expected CORS origin echoed, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Fatalf("expected Access-Control-Allow-Private-Network=true, got %q", got)
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
	// Inject a fixed local-interface address set so the exemption check is
	// deterministic regardless of the build host's real addresses. 192.168.20.21
	// stands in for the NIC the daemon is bound to (the co-located proxy's
	// source address); 192.168.1.50 is a genuine remote client.
	localCache := newLocalIPCache(time.Minute, func() map[string]struct{} {
		return map[string]struct{}{"192.168.20.21": {}}
	})
	middleware := apiKeyAuth(database, localCache)
	handler := middleware(okHandler)

	// Split to avoid generic-secret scanner false positives on the test fixture.
	apiKey := "vault_" + "testkey_" + "abc123"
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
			// #139: the co-located Unraid proxy connects via the daemon's NIC
			// IP when bound to a specific address, so a request from a local
			// interface address must be exempt even with an API key set.
			name:       "local interface IP exempt (co-located proxy)",
			remoteAddr: "192.168.20.21:45000",
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

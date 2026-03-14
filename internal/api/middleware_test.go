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

func TestAPIKeyAuth(t *testing.T) {
	t.Parallel()

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	tests := []struct {
		name       string
		apiKey     string
		header     string
		headerVal  string
		queryToken string
		wantStatus int
	}{
		{
			name:       "no key configured allows all",
			apiKey:     "",
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid bearer token",
			apiKey:     "test-secret-key",
			header:     "Authorization",
			headerVal:  "Bearer test-secret-key",
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid x-api-key",
			apiKey:     "test-secret-key",
			header:     "X-API-Key",
			headerVal:  "test-secret-key",
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid query token",
			apiKey:     "test-secret-key",
			queryToken: "test-secret-key",
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing key returns 401",
			apiKey:     "test-secret-key",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong key returns 401",
			apiKey:     "test-secret-key",
			header:     "Authorization",
			headerVal:  "Bearer wrong-key",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong x-api-key returns 401",
			apiKey:     "test-secret-key",
			header:     "X-API-Key",
			headerVal:  "wrong-key",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mw := APIKeyAuth(func() string { return tt.apiKey })
			handler := mw(okHandler)

			path := "/"
			if tt.queryToken != "" {
				path = "/?token=" + tt.queryToken
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if tt.header != "" {
				req.Header.Set(tt.header, tt.headerVal)
			}

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
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

func TestTrustedLocalProxyRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(trustedProxyHeader, trustedProxyValue)
	req.RemoteAddr = "192.168.1.25:1234"

	if !isTrustedLocalProxyRequest(req, "192.168.1.25:24085") {
		t.Fatal("expected trusted proxy request to be accepted")
	}
}

func TestTrustedLocalProxyRequestRejectsRemoteSource(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(trustedProxyHeader, trustedProxyValue)
	req.RemoteAddr = "203.0.113.10:1234"

	if isTrustedLocalProxyRequest(req, "192.168.1.25:24085") {
		t.Fatal("expected remote proxy request to be rejected")
	}
}

func TestLocalUIBypass(t *testing.T) {
	t.Parallel()

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	tests := []struct {
		name         string
		apiKey       string
		secFetchSite string
		origin       string
		referer      string
		remoteAddr   string
		authHeader   string
		wantStatus   int
	}{
		{
			name:         "same-origin bypasses auth",
			apiKey:       "test-secret-key",
			secFetchSite: "same-origin",
			wantStatus:   http.StatusOK,
		},
		{
			name:         "same-origin bypasses even without key configured",
			apiKey:       "",
			secFetchSite: "same-origin",
			wantStatus:   http.StatusOK,
		},
		{
			name:         "cross-site requires key",
			apiKey:       "test-secret-key",
			secFetchSite: "cross-site",
			wantStatus:   http.StatusUnauthorized,
		},
		{
			name:         "cross-site with valid key passes",
			apiKey:       "test-secret-key",
			secFetchSite: "cross-site",
			authHeader:   "Bearer test-secret-key",
			wantStatus:   http.StatusOK,
		},
		{
			name:       "no sec-fetch-site requires key",
			apiKey:     "test-secret-key",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "no sec-fetch-site with valid key passes",
			apiKey:     "test-secret-key",
			authHeader: "Bearer test-secret-key",
			wantStatus: http.StatusOK,
		},
		{
			name:       "same-origin origin bypasses auth",
			apiKey:     "test-secret-key",
			origin:     "http://example.com",
			remoteAddr: "example.com:1234",
			wantStatus: http.StatusOK,
		},
		{
			name:       "same-origin referer bypasses auth",
			apiKey:     "test-secret-key",
			referer:    "http://example.com/#/settings",
			remoteAddr: "example.com:1234",
			wantStatus: http.StatusOK,
		},
		{
			name:       "loopback bypasses auth",
			apiKey:     "test-secret-key",
			remoteAddr: "127.0.0.1:1234",
			wantStatus: http.StatusOK,
		},
		{
			name:       "non-loopback requires auth",
			apiKey:     "test-secret-key",
			remoteAddr: "192.168.1.25:1234",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:         "none origin requires key",
			apiKey:       "test-secret-key",
			secFetchSite: "none",
			wantStatus:   http.StatusUnauthorized,
		},
		{
			name:       "no key configured allows all regardless",
			apiKey:     "",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mw := LocalUIBypass(func() string { return tt.apiKey }, "127.0.0.1:24085")
			handler := mw(okHandler)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.secFetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.secFetchSite)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.referer != "" {
				req.Header.Set("Referer", tt.referer)
			}
			if tt.remoteAddr != "" {
				req.RemoteAddr = tt.remoteAddr
			}
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestAdminBoundary(t *testing.T) {
	t.Parallel()

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	tests := []struct {
		name         string
		apiKey       string
		secFetchSite string
		origin       string
		referer      string
		remoteAddr   string
		authHeader   string
		wantStatus   int
	}{
		{
			name:         "same-origin always allowed",
			apiKey:       "secret",
			secFetchSite: "same-origin",
			wantStatus:   http.StatusOK,
		},
		{
			name:         "same-origin allowed even with no key configured",
			apiKey:       "",
			secFetchSite: "same-origin",
			wantStatus:   http.StatusOK,
		},
		{
			name:       "no key configured + no same-origin = 403",
			apiKey:     "",
			wantStatus: http.StatusForbidden,
		},
		{
			name:         "cross-site with no key configured = 403",
			apiKey:       "",
			secFetchSite: "cross-site",
			wantStatus:   http.StatusForbidden,
		},
		{
			name:       "key configured + no token = 401",
			apiKey:     "secret",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "key configured + valid token = 200",
			apiKey:     "secret",
			authHeader: "Bearer secret",
			wantStatus: http.StatusOK,
		},
		{
			name:       "same-origin origin allowed without key configured",
			apiKey:     "",
			origin:     "http://example.com",
			remoteAddr: "example.com:1234",
			wantStatus: http.StatusOK,
		},
		{
			name:       "same-origin referer allowed without key configured",
			apiKey:     "",
			referer:    "http://example.com/#/settings",
			remoteAddr: "example.com:1234",
			wantStatus: http.StatusOK,
		},
		{
			name:       "loopback allowed without key configured",
			apiKey:     "",
			remoteAddr: "127.0.0.1:1234",
			wantStatus: http.StatusOK,
		},
		{
			name:       "non-loopback is forbidden when no key configured",
			apiKey:     "",
			remoteAddr: "192.168.1.25:1234",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "key configured + wrong token = 401",
			apiKey:     "secret",
			authHeader: "Bearer wrong",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:         "cross-site + key configured + valid token = 200",
			apiKey:       "secret",
			secFetchSite: "cross-site",
			authHeader:   "Bearer secret",
			wantStatus:   http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mw := AdminBoundary(func() string { return tt.apiKey }, "127.0.0.1:24085")
			handler := mw(okHandler)

			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.secFetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.secFetchSite)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.referer != "" {
				req.Header.Set("Referer", tt.referer)
			}
			if tt.remoteAddr != "" {
				req.RemoteAddr = tt.remoteAddr
			}
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
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
		if !strings.Contains(got, "api: GET /missing, status=404") {
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
		if !strings.Contains(got, "api: GET /api/v1/storage, status=200") {
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

	const listed = "https://ha.example"
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := PrivateNetworkAccess(newOriginSet([]string{listed}))(okHandler)

	tests := []struct {
		name      string
		method    string
		origin    string
		pnaHeader string
		wantAllow bool
	}{
		{"listed origin gets allow header", http.MethodOptions, listed, "true", true},
		{"unlisted origin gets nothing", http.MethodOptions, "https://attacker.example", "true", false},
		{"no origin gets nothing", http.MethodOptions, "", "true", false},
		{"preflight without PNA request header is untouched", http.MethodOptions, listed, "", false},
		{"non-preflight request is untouched", http.MethodGet, listed, "true", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tt.method, "/api/v1/health", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
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
	middleware := apiKeyAuth(database, localCache, newAPIKeyVerifier(), map[string]struct{}{"tower": {}, "localhost": {}})
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
		host       string
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
			name:       "no key configured refuses remote",
			remoteAddr: "192.168.1.50:12345",
			setupKey:   false,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "presented key refused when none configured",
			remoteAddr: "192.168.1.50:12345",
			header:     apiKey,
			setupKey:   false,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "machine hostname keeps local trust",
			remoteAddr: "127.0.0.1:12345",
			host:       "TOWER:24085",
			setupKey:   true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "foreign hostname on loopback loses local trust",
			remoteAddr: "127.0.0.1:12345",
			host:       "attacker.example",
			setupKey:   true,
			wantStatus: http.StatusUnauthorized,
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
			req.Host = "127.0.0.1:24085"
			if tt.host != "" {
				req.Host = tt.host
			}
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

func newTestVerifier(compare func(key, hash string) error) *apiKeyVerifier {
	v := newAPIKeyVerifier()
	v.compare = compare
	return v
}

func verifierRequest(addr string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	r.RemoteAddr = addr
	return r
}

// Issue #564: a client holding the key pays for one comparison, and rotating the
// stored hash forces a fresh one.
func TestAPIKeyVerifierCachesValidKeyPerHash(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	v := newTestVerifier(func(_, _ string) error { calls.Add(1); return nil })
	for range 5 {
		if got := v.check(httptest.NewRecorder(), verifierRequest("192.0.2.1:1"), "key", "hash-1"); got != keyValid {
			t.Fatalf("verdict = %v, want valid", got)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("compare ran %d times, want 1", calls.Load())
	}
	if got := v.check(httptest.NewRecorder(), verifierRequest("192.0.2.1:1"), "key", "hash-2"); got != keyValid {
		t.Fatalf("verdict after rotation = %v, want valid", got)
	}
	if calls.Load() != 2 {
		t.Errorf("compare ran %d times after a hash change, want 2", calls.Load())
	}
}

// Issue #564: bad guesses from one address stop reaching bcrypt.
func TestAPIKeyVerifierLimitsAttemptsPerAddress(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	v := newTestVerifier(func(_, _ string) error { calls.Add(1); return errors.New("nope") })
	for range apiKeyAttemptsPerMinute + 20 {
		v.check(httptest.NewRecorder(), verifierRequest("192.0.2.1:1"), "wrong", "hash")
	}
	if got := calls.Load(); got > apiKeyAttemptsPerMinute+1 {
		t.Errorf("compare ran %d times, want at most %d", got, apiKeyAttemptsPerMinute+1)
	}
	if got := v.check(httptest.NewRecorder(), verifierRequest("192.0.2.1:1"), "wrong", "hash"); got != keyThrottled {
		t.Errorf("verdict past the limit = %v, want throttled", got)
	}
	if got := v.check(httptest.NewRecorder(), verifierRequest("192.0.2.2:1"), "wrong", "hash"); got != keyInvalid {
		t.Errorf("another address = %v, want invalid", got)
	}
}

// Issue #564: distributed clients can't run more comparisons at once than there
// are slots, the excess is refused, and a known key is still served.
func TestAPIKeyVerifierBoundsConcurrency(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var running, peak atomic.Int32
	v := newTestVerifier(func(key, _ string) error {
		if key == "good" {
			return nil
		}
		n := running.Add(1)
		defer running.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		return errors.New("nope")
	})
	v.slotWait = 20 * time.Millisecond

	if got := v.check(httptest.NewRecorder(), verifierRequest("192.0.2.200:1"), "good", "hash"); got != keyValid {
		t.Fatalf("seeding good key = %v", got)
	}

	var wg sync.WaitGroup
	verdicts := make(chan keyVerdict, 8)
	for i := range 8 {
		wg.Go(func() {
			verdicts <- v.check(httptest.NewRecorder(), verifierRequest(fmt.Sprintf("192.0.2.%d:1", i+1)), "bad", "hash")
		})
	}
	throttled := 0
	for range 8 - apiKeyVerifySlots {
		if <-verdicts == keyThrottled {
			throttled++
		}
	}
	if got := v.check(httptest.NewRecorder(), verifierRequest("192.0.2.200:1"), "good", "hash"); got != keyValid {
		t.Errorf("known key during a flood = %v, want valid", got)
	}
	close(release)
	wg.Wait()
	if peak.Load() > apiKeyVerifySlots {
		t.Errorf("peak concurrent comparisons = %d, want at most %d", peak.Load(), apiKeyVerifySlots)
	}
	if throttled != 8-apiKeyVerifySlots {
		t.Errorf("throttled = %d, want %d", throttled, 8-apiKeyVerifySlots)
	}
}

func TestAPIKeyVerifierGivesUpWhenClientLeaves(t *testing.T) {
	t.Parallel()
	v := newTestVerifier(func(_, _ string) error { return nil })
	v.slotWait = time.Hour
	for range apiKeyVerifySlots {
		v.slots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := v.check(httptest.NewRecorder(), verifierRequest("192.0.2.1:1").WithContext(ctx), "key", "hash")
	if got != keyThrottled {
		t.Errorf("verdict = %v, want throttled", got)
	}
}

// The middleware reports throttling as 429 rather than a misleading 401.
func TestAPIKeyAuthReportsThrottling(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	if err := database.SetSetting("api_key_hash", "stored-hash"); err != nil {
		t.Fatal(err)
	}
	v := newTestVerifier(func(_, _ string) error { return errors.New("nope") })
	v.slotWait = time.Millisecond
	for range apiKeyVerifySlots {
		v.slots <- struct{}{}
	}
	local := newLocalIPCache(time.Minute, func() map[string]struct{} { return nil })
	h := apiKeyAuth(database, local, v, nil)(http.NotFoundHandler())
	req := verifierRequest("192.0.2.1:1")
	req.Header.Set("X-API-Key", "guess")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}

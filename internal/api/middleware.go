package api

import (
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
)

const (
	// maxRequestBodySize is the default maximum request body size (1 MB).
	maxRequestBodySize int64 = 1 << 20

	// slowRequestLogThreshold preserves a small amount of request visibility
	// without logging every successful poll and asset fetch.
	slowRequestLogThreshold = 2 * time.Second
)

// QuietRequestLogger only logs requests that are slow or unsuccessful.
func QuietRequestLogger(next http.Handler) http.Handler {
	return newQuietRequestLogger(log.Default(), slowRequestLogThreshold)(next)
}

func newQuietRequestLogger(logger *log.Logger, slowThreshold time.Duration) func(http.Handler) http.Handler {
	if logger == nil {
		logger = log.Default()
	}
	if slowThreshold <= 0 {
		slowThreshold = slowRequestLogThreshold
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			duration := time.Since(start)
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}

			if status < http.StatusBadRequest && duration < slowThreshold {
				return
			}

			logger.Printf(
				"api: %s %s status=%d bytes=%d duration=%s remote=%s",
				r.Method,
				requestPath(r),
				status,
				ww.BytesWritten(),
				duration.Round(time.Millisecond),
				remoteAddrHost(r.RemoteAddr),
			)
		})
	}
}

func requestPath(r *http.Request) string {
	if r.URL == nil || r.URL.Path == "" {
		return "/"
	}

	return r.URL.Path
}

func remoteAddrHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		addr = host
	}

	addr = strings.Trim(addr, "[]")
	if addr == "" {
		return "-"
	}

	return addr
}

// ReadOnlyGuard returns middleware that blocks non-GET/HEAD requests in
// replica mode. Used to prevent write operations on read-only replicas.
func ReadOnlyGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"this is a read-only replica"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BodySizeLimit returns middleware that limits the request body to maxBytes.
// Requests exceeding the limit receive a 413 Payload Too Large response.
func BodySizeLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// APIKeyAuth returns middleware that enforces API key authentication for
// non-loopback requests. Loopback connections (127.0.0.1, ::1) and the
// Unraid PHP proxy are always exempt — authentication only applies when
// the daemon is exposed on a LAN address (bind != 127.0.0.1).
//
// The middleware checks the X-API-Key header against the bcrypt hash stored
// in the settings table. If no API key has been generated, all requests pass.
func APIKeyAuth(database *db.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Loopback connections are always exempt.
			if isLoopback(r.RemoteAddr) {
				next.ServeHTTP(w, r)
				return
			}

			// OAuth callback routes cannot carry API keys because the
			// browser is redirected by the provider. Exempt them.
			if isOAuthCallback(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// Check if an API key has been configured.
			hash, err := database.GetSetting("api_key_hash", "")
			if err != nil {
				// Fail closed: DB errors must not allow unauthenticated access.
				respondUnauthorized(w)
				return
			}
			if hash == "" {
				// No API key set — all requests pass.
				next.ServeHTTP(w, r)
				return
			}

			// Extract key from X-API-Key header.
			key := r.Header.Get("X-API-Key")
			if key == "" {
				respondUnauthorized(w)
				return
			}

			// Constant-time comparison via bcrypt.
			if err := crypto.VerifyPassphrase(key, hash); err != nil {
				respondUnauthorized(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// isLoopback returns true if the remote address is a loopback address
// (127.0.0.0/8 or ::1). This exempts local UI and Unraid PHP proxy requests.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// oauthCallbackSuffixes are URL path suffixes that indicate an OAuth provider
// redirect. These cannot carry API keys, so they must be exempt.
var oauthCallbackSuffixes = []string{
	"/gdrive/callback",
	"/onedrive/callback",
}

// isOAuthCallback returns true when the request path ends with a known
// OAuth callback suffix.
func isOAuthCallback(path string) bool {
	for _, suffix := range oauthCallbackSuffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func respondUnauthorized(w http.ResponseWriter) {
	respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "valid API key required"})
}

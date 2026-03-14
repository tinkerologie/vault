package api

import (
	"crypto/subtle"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

const (
	// maxRequestBodySize is the default maximum request body size (1 MB).
	maxRequestBodySize int64 = 1 << 20

	// slowRequestLogThreshold preserves a small amount of request visibility
	// without logging every successful poll and asset fetch.
	slowRequestLogThreshold = 2 * time.Second
)

const (
	trustedProxyHeader = "X-Vault-Proxy"
	trustedProxyValue  = "unraid-plugin-proxy"
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

// KeyResolver returns the current API key. If it returns an empty string,
// authentication is not required.
type KeyResolver func() string

// APIKeyAuth returns middleware that enforces API key authentication.
// The resolver is called on each request to get the current key, supporting
// dynamic key rotation without server restart. If the resolver returns an
// empty string, all requests pass through.
func APIKeyAuth(resolve KeyResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := resolve()
			if apiKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			token := extractToken(r)
			if token == "" {
				http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
				return
			}

			if subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
				http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// LocalUIBypass returns middleware that skips API key authentication for
// same-origin browser requests (the Vault Web UI). External clients (curl,
// Home Assistant, etc.) must still provide a valid API key.
//
// Detection accepts either same-origin browser metadata, a trusted local proxy
// request from the Unraid PHP layer, or a loopback request used by local
// verification helpers.
func LocalUIBypass(resolve KeyResolver, listenAddr string) func(http.Handler) http.Handler {
	apiKeyAuth := APIKeyAuth(resolve)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isTrustedUIRequest(r, listenAddr) {
				next.ServeHTTP(w, r)
				return
			}

			// Not a same-origin browser request — fall through to
			// standard API key authentication.
			apiKeyAuth(next).ServeHTTP(w, r)
		})
	}
}

// AdminBoundary returns middleware that allows trusted browser or local proxy
// requests through unconditionally, but enforces API key authentication for all
// other requests — even when no API key is currently configured. This prevents
// arbitrary external clients from calling admin-only endpoints (key rotation,
// key revocation) when authentication has not yet been set up.
//
// Unlike LocalUIBypass, this does NOT fall back to "allow-all" when no key is
// configured, ensuring these security-sensitive endpoints are browser-only
// unless the caller presents a valid key.
func AdminBoundary(resolve KeyResolver, listenAddr string) func(http.Handler) http.Handler {
	apiKeyAuth := APIKeyAuth(resolve)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isTrustedUIRequest(r, listenAddr) {
				next.ServeHTTP(w, r)
				return
			}

			// Non-same-origin: require a valid API key.
			// If no key is configured we still block rather than allow-all,
			// because these routes are not intended for external clients.
			apiKey := resolve()
			if apiKey == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"error":"this endpoint is only accessible from the browser UI or with a valid API key"}`)) //nolint:errcheck
				return
			}

			// Key is configured — delegate to normal API key auth.
			apiKeyAuth(next).ServeHTTP(w, r)
		})
	}
}

// isTrustedUIRequest identifies requests originating from the built-in web UI,
// a trusted local proxy on the same host, or loopback verification helpers.
func isTrustedUIRequest(r *http.Request, listenAddr string) bool {
	if isTrustedLocalProxyRequest(r, listenAddr) {
		return true
	}

	if isTrustedLoopbackRequest(r) {
		return true
	}

	if r.Header.Get("Sec-Fetch-Site") == "same-origin" {
		return true
	}

	requestOrigin := effectiveRequestOrigin(r)
	if requestOrigin == "" {
		return false
	}

	if origin := r.Header.Get("Origin"); origin != "" && sameOrigin(origin, requestOrigin) {
		return true
	}

	if referer := r.Header.Get("Referer"); referer != "" && sameOrigin(referer, requestOrigin) {
		return true
	}

	return false
}

func isTrustedLocalProxyRequest(r *http.Request, listenAddr string) bool {
	return isTrustedLocalProxyRequestWithMatcher(r, func(req *http.Request) bool {
		return requestComesFromTrustedBind(req, listenAddr)
	})
}

func isTrustedLocalProxyRequestWithMatcher(r *http.Request, matcher func(*http.Request) bool) bool {
	if !strings.EqualFold(r.Header.Get(trustedProxyHeader), trustedProxyValue) {
		return false
	}

	return matcher(r)
}

func isTrustedLoopbackRequest(r *http.Request) bool {
	host := r.RemoteAddr
	if parsedHost, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = parsedHost
	}

	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func requestComesFromTrustedBind(r *http.Request, listenAddr string) bool {
	remoteIP := ipFromAddr(r.RemoteAddr)
	if remoteIP == nil {
		return false
	}
	if remoteIP.IsLoopback() {
		return true
	}

	bindIP := bindAddrIP(listenAddr)
	if bindIP == nil {
		return false
	}

	return remoteIP.Equal(bindIP)
}

func ipFromAddr(addr string) net.IP {
	host := addr
	if parsedHost, _, err := net.SplitHostPort(addr); err == nil {
		host = parsedHost
	}

	return net.ParseIP(strings.Trim(host, "[]"))
}

func bindAddrIP(listenAddr string) net.IP {
	host := listenAddr
	if parsedHost, _, err := net.SplitHostPort(listenAddr); err == nil {
		host = parsedHost
	}

	normalized := strings.Trim(strings.TrimSpace(host), "[]")
	if normalized == "" || strings.EqualFold(normalized, "localhost") {
		return nil
	}

	ip := net.ParseIP(normalized)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return nil
	}

	return ip
}

func effectiveRequestOrigin(r *http.Request) string {
	host := r.Host
	if host == "" {
		return ""
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if forwardedProto := r.Header.Get("X-Forwarded-Proto"); forwardedProto != "" {
		scheme = forwardedProto
	}

	return scheme + "://" + host
}

func sameOrigin(candidate string, expected string) bool {
	u, err := url.Parse(candidate)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}

	return strings.EqualFold(u.Scheme+"://"+u.Host, expected)
}

// extractToken extracts the API key from the request.
// It checks (in order): Authorization Bearer header, X-API-Key header,
// and the "token" query parameter (for WebSocket connections).
func extractToken(r *http.Request) string {
	// Authorization: Bearer <key>
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimPrefix(auth, "Bearer ")
		}
	}

	// X-API-Key: <key>
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}

	// Query param for WebSocket connections.
	if token := r.URL.Query().Get("token"); token != "" {
		return token
	}

	return ""
}

// ReadOnlyGuard returns middleware that blocks non-GET/HEAD requests in
// replica mode. Used to prevent write operations on read-only replicas.
func ReadOnlyGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"this is a read-only replica"}`)) //nolint:errcheck
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

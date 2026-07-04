package api

import (
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/docsmeta"
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

			// Logged fields are deliberately limited to method, path, status, bytes,
			// duration, and remote host. DO NOT add request or response headers here:
			// authorization headers (X-API-Key, Authorization, Cookie, etc.) MUST NOT
			// be logged. If header logging is added in future, redact the following
			// keys: authorization, cookie, set-cookie, x-api-key, proxy-authorization.
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
// requests that do not originate from the daemon's own host. Requests from the
// host itself — loopback (127.0.0.1, ::1) or any local interface address — are
// always exempt, which keeps the co-located Unraid PHP proxy working whether
// the daemon is bound to loopback, a wildcard, or a specific NIC IP. API key
// authentication only applies to genuine remote clients.
//
// The middleware checks the X-API-Key header against the bcrypt hash stored
// in the settings table. If no API key has been generated, all requests pass.
func APIKeyAuth(database *db.DB) func(http.Handler) http.Handler {
	return apiKeyAuth(database, newLocalIPCache(localIPCacheTTL, readLocalInterfaceIPs))
}

// apiKeyAuth is the testable core of APIKeyAuth. The localIPCache identifies
// addresses that count as "this host" (in addition to loopback) and are
// therefore exempt from authentication.
func apiKeyAuth(database *db.DB, local *localIPCache) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Requests from the daemon's own host are always exempt. The
			// Unraid PHP proxy connects to whichever address the daemon is
			// bound to, so on a specific NIC bind its source address is that
			// NIC IP rather than loopback — without this the entire proxied
			// UI returns 401 once an API key is set (#139).
			if isLocalRequest(r.RemoteAddr, local) {
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
			hash, err := database.GetSetting("api_key_hash", docsmeta.DefaultFor("api_key_hash"))
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

// isLocalRequest reports whether the request originates from the host running
// the daemon: a loopback address, or any address assigned to a local network
// interface. A genuine remote LAN client's source IP is never one of the
// host's own addresses, so it is not treated as local.
func isLocalRequest(remoteAddr string, local *localIPCache) bool {
	if isLoopback(remoteAddr) {
		return true
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	host = strings.Trim(host, "[]")
	return local.has(net.ParseIP(host))
}

// localIPCacheTTL bounds how often the host's interface addresses are
// re-enumerated for the API-key exemption check.
const localIPCacheTTL = 30 * time.Second

// localIPCache memoises the set of IP addresses assigned to this host's
// network interfaces, refreshing at most once per ttl. It lets the API-key
// middleware treat requests from the host itself (the co-located Unraid PHP
// proxy) as local without enumerating interfaces on every request. The lookup
// is injectable so tests can supply a deterministic address set.
type localIPCache struct {
	ttl     time.Duration
	lookup  func() map[string]struct{}
	mu      sync.Mutex
	ips     map[string]struct{}
	expires time.Time
}

func newLocalIPCache(ttl time.Duration, lookup func() map[string]struct{}) *localIPCache {
	return &localIPCache{ttl: ttl, lookup: lookup}
}

// has reports whether ip is currently assigned to a local interface.
func (c *localIPCache) has(ip net.IP) bool {
	if ip == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ips == nil || time.Now().After(c.expires) {
		c.ips = c.lookup()
		c.expires = time.Now().Add(c.ttl)
	}
	_, ok := c.ips[ip.String()]
	return ok
}

// readLocalInterfaceIPs returns the set of IP addresses assigned to the host's
// network interfaces, keyed by their canonical string form.
func readLocalInterfaceIPs() map[string]struct{} {
	set := make(map[string]struct{})
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return set
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok {
			set[ipNet.IP.String()] = struct{}{}
		}
	}
	return set
}

// isOAuthCallback returns true when the request path ends with a known
// OAuth callback suffix. Currently unused — reserved for future OAuth
// storage providers.
func isOAuthCallback(_ string) bool {
	return false
}

func respondUnauthorized(w http.ResponseWriter) {
	respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "valid API key required"})
}

package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/docsmeta"
	"github.com/tinkerologie/vault/internal/restorerequest"
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
				"api: %s %s, status=%d, bytes=%d, duration=%s, remote=%s",
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

	if strings.Contains(r.URL.Path, crypto.AssistantKeyPrefix) {
		return "[redacted path]"
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

// guardReplica returns ReadOnlyGuard on a replica and a pass-through elsewhere.
func guardReplica(readOnly bool) func(http.Handler) http.Handler {
	if readOnly {
		return ReadOnlyGuard
	}
	return func(next http.Handler) http.Handler { return next }
}

// guardReplicaFileCleanup blocks deleting a destination's backup files on a
// replica, and force-deleting one with dependent jobs, which a replica could
// never point back at a destination. Removing an unused destination's record
// stays allowed, as a replica's destinations are configured through the API.
func guardReplicaFileCleanup(readOnly bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		guarded := ReadOnlyGuard(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if readOnly && (q.Get("deleteFiles") == "true" || q.Get("force") == "true") {
				guarded.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
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

// PrivateNetworkAccess answers Chrome/Brave Private Network Access (PNA)
// preflights for origins the operator listed. go-chi/cors does not emit this
// header, so add it here. Registered outside cors.Handler so the header
// survives the preflight response (issue #250). Being a PNA preflight earns
// nothing by itself: an unlisted origin gets no grant (issue #544).
func PrivateNetworkAccess(allowed originSet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions &&
				r.Header.Get("Access-Control-Request-Private-Network") == "true" &&
				allowed.has(r.Header.Get("Origin")) {
				w.Header().Set("Access-Control-Allow-Private-Network", "true")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// originSet is the operator's exact-match allowlist of browser origins, keyed
// by lower-case scheme://host[:port].
type originSet map[string]struct{}

func (o originSet) has(origin string) bool {
	_, ok := o[strings.ToLower(origin)]
	return ok
}

func newOriginSet(origins []string) originSet {
	set := make(originSet, len(origins))
	for _, origin := range origins {
		set[origin] = struct{}{}
	}
	return set
}

// parseAllowedOrigins normalises configured origins. Only an exact http(s)
// scheme://host[:port] is accepted, so wildcards, "null" and anything carrying
// a path or credentials land in invalid.
func parseAllowedOrigins(raw []string) (valid, invalid []string) {
	for _, entry := range raw {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		u, err := url.Parse(entry)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			strings.Contains(u.Host, "*") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			invalid = append(invalid, entry)
			continue
		}
		valid = append(valid, u.Scheme+"://"+u.Host)
	}
	return valid, invalid
}

// isCrossOrigin reports whether a browser sent this request on behalf of a page
// from another origin. Non-browser clients (the PHP proxy, curl, the CLI, MCP
// bridges) send neither header and are never cross-origin.
func isCrossOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// A TLS-terminating proxy makes an https page look like this: its origin
	// must be listed, because a forwarded-scheme header can't be trusted here.
	return !strings.EqualFold(u.Scheme, scheme) || !strings.EqualFold(u.Host, r.Host)
}

// CrossOriginGuard refuses requests from pages on an origin the operator has
// not listed, on every method. CORS alone can't do this, since it only hides
// responses and a cross-site form or no-cors fetch still reaches the handler.
func CrossOriginGuard(allowed originSet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isCrossOrigin(r) && !allowed.has(r.Header.Get("Origin")) {
				respondJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request refused"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ExtendWriteDeadline returns route-scoped middleware that pushes the
// connection's write deadline out to now+d for one request, so a legitimately
// slow handler can outlive the server-wide WriteTimeout without raising it for
// every route (issue #449).
//
// The read deadline is deliberately left alone: for a request without a body,
// net/http clears it when the handler starts (connReader.startBackgroundRead),
// so ReadTimeout never cuts a slow handler short, and setting one here would
// add a context cancellation that does not otherwise exist.
//
// Writers that cannot set deadlines (e.g. httptest.ResponseRecorder) return
// http.ErrNotSupported; the request is still served under the default limit.
func ExtendWriteDeadline(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
			if err != nil && !errors.Is(err, http.ErrNotSupported) {
				log.Printf("api: extend write deadline for %s: %v", requestPath(r), err) // #nosec G706 //nolint:gosec // path only, no headers or credentials
			}
			next.ServeHTTP(w, r)
		})
	}
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

// accessLevel records how a request earned access, for handlers and route
// middleware that treat callers differently.
type accessLevel int

const (
	accessNone      accessLevel = iota
	accessAPIKey                // presented the configured API key
	accessAssistant             // presented an inspection-only Assistant Key
	accessLocal                 // the daemon's own host, not a cross-origin page
)

type principalKey struct{}

type principal struct {
	level           accessLevel
	assistantID     int64
	assistantDigest string
	restoreGrant    string
}

func requestPrincipal(r *http.Request) principal {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p
}

func requestAccess(r *http.Request) accessLevel {
	return requestPrincipal(r).level
}

func withPrincipal(r *http.Request, p principal) *http.Request {
	ctx := context.WithValue(r.Context(), principalKey{}, p)
	return r.WithContext(restorerequest.WithPrincipal(ctx, restorerequest.Principal{
		AssistantDigest: p.assistantDigest, LocalReview: p.level == accessLocal,
	}))
}

func withAccess(r *http.Request, level accessLevel) *http.Request {
	return withPrincipal(r, principal{level: level})
}

// LocalOnly restricts a route to the daemon's own host. It covers routes that
// return or replace a secret, so a leaked API key can't be turned into the
// backup passphrase, the server key, or a permanent replacement for itself.
func LocalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestAccess(r) != accessLocal {
			respondJSON(w, http.StatusForbidden, map[string]string{"error": "only available from the Vault host: use the Unraid web UI, an SSH tunnel, or the vault CLI"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// APIKeyAuth returns the middleware that decides who reaches the API. It denies
// by default:
//
//   - The daemon's own host is trusted without a key, which keeps the co-located
//     Unraid PHP proxy and the CLI working whether the daemon is bound to
//     loopback, a wildcard, or a specific NIC IP. A browser page on another
//     origin gets no such trust, and neither does a Host name that isn't this
//     machine's, since that is how DNS rebinding reaches loopback.
//   - Everyone else, which in practice means replication peers and Home
//     Assistant, must present the configured API key. With no key configured they
//     get 401 and a message saying where to generate one.
func APIKeyAuth(database *db.DB, revocations *db.AssistantRevocations) func(http.Handler) http.Handler {
	return apiKeyAuth(database, newLocalIPCache(localIPCacheTTL, readLocalInterfaceIPs), newAPIKeyVerifier(), localHostNames(), revocations)
}

// apiKeyAuth is the testable core of APIKeyAuth. The localIPCache identifies
// addresses that count as "this host" (in addition to loopback); hostNames are
// the Host names that may claim local trust.
func apiKeyAuth(database *db.DB, local *localIPCache, verifier *apiKeyVerifier, hostNames map[string]struct{}, revocations *db.AssistantRevocations) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, presented := presentedAssistantKey(r.Header.Values("X-API-Key"))
			legacyAdmin := crypto.IsLegacyAdminKeyWithAssistantPrefix(key)
			if presented && !legacyAdmin {
				digest, valid := crypto.AssistantKeyDigest(key)
				if valid && revocations.Permits(digest) {
					key, err := database.FindAssistantKey(r.Context(), digest)
					if err == nil && revocations.Permits(digest) {
						p := principal{level: accessAssistant, assistantID: key.ID, assistantDigest: digest}
						if revocations.PermitsRestoreRequests(key) {
							p.restoreGrant = key.RestoreGrant
						}
						next.ServeHTTP(w, withPrincipal(r, p))
						return
					}
				}
				verifier.rejectAssistantKey(w, r)
				return
			}

			// The Unraid PHP proxy connects to whichever address the daemon is
			// bound to, so on a specific NIC bind its source address is that
			// NIC IP rather than loopback (#139).
			localAccess := isLocalRequest(r.RemoteAddr, local) && !isCrossOrigin(r) && isLocalHostHeader(r.Host, hostNames)
			if localAccess && !legacyAdmin {
				next.ServeHTTP(w, withAccess(r, accessLocal))
				return
			}

			// OAuth callback routes cannot carry API keys because the
			// browser is redirected by the provider. Exempt them.
			if isOAuthCallback(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// An unreadable hash refuses: DB errors must not allow access.
			hash, err := database.GetSetting("api_key_hash", docsmeta.DefaultFor("api_key_hash"))
			if err != nil {
				respondUnauthorized(w)
				return
			}
			if hash == "" {
				if legacyAdmin {
					verifier.rejectAssistantKey(w, r)
					return
				}
				respondJSON(w, http.StatusUnauthorized, map[string]string{"error": unconfiguredRefusal})
				return
			}
			key = r.Header.Get("X-API-Key")
			if key == "" {
				respondUnauthorized(w)
				return
			}

			switch verifier.check(w, r, key, hash) {
			case keyValid:
				level := accessAPIKey
				if localAccess {
					level = accessLocal
				}
				next.ServeHTTP(w, withAccess(r, level))
			case keyThrottled:
				respondKeyThrottled(w)
			default:
				respondUnauthorized(w)
			}
		})
	}
}

// A repeated or comma-combined header mentioning an Assistant Key is ambiguous.
// Refuse it in the scoped branch instead of treating it as keyless local access.
func presentedAssistantKey(values []string) (string, bool) {
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			key := strings.TrimSpace(part)
			if strings.HasPrefix(key, crypto.AssistantKeyPrefix) {
				if len(values) != 1 || value != key {
					return "", true
				}
				return key, true
			}
		}
	}
	return "", false
}

func (v *apiKeyVerifier) rejectAssistantKey(w http.ResponseWriter, r *http.Request) {
	client, _ := keyByRemoteAddr(r)
	if v.attempts.OnLimit(w, r, client) {
		respondKeyThrottled(w)
		return
	}
	respondUnauthorized(w)
}

func respondKeyThrottled(w http.ResponseWriter) {
	respondJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many API key attempts, try again later"})
}

// unconfiguredRefusal is the refusal when no key exists. A key is only needed for
// replication and Home Assistant, so the message tells that user where to go.
const unconfiguredRefusal = "no API key configured: generate one in the Unraid web UI under Vault Settings, Security, API Access"

const (
	// apiKeyAttemptsPerMinute limits bcrypt comparisons per client address.
	apiKeyAttemptsPerMinute = 30
	// apiKeyVerifySlots caps concurrent bcrypt comparisons across all clients,
	// which is what stops a distributed flood from taking every CPU.
	apiKeyVerifySlots = 2
	// apiKeySlotWait is how long a request queues for a slot before it is
	// refused. It comfortably covers a cold-start burst from one client.
	apiKeySlotWait = 2 * time.Second
)

type keyVerdict int

const (
	keyInvalid keyVerdict = iota
	keyValid
	keyThrottled
)

// apiKeyVerifier bounds the bcrypt work an unauthenticated client can cause
// (issue #564). The last verified key is remembered as a digest, so a client
// that holds the key costs one comparison rather than one per request and keeps
// working during a flood. The entry is tied to the stored hash, so generating,
// rotating or revoking a key invalidates it.
type apiKeyVerifier struct {
	compare  func(key, hash string) error
	attempts *httprate.RateLimiter
	slots    chan struct{}
	slotWait time.Duration

	mu           sync.Mutex
	cachedHash   string
	cachedDigest [sha256.Size]byte
}

func newAPIKeyVerifier() *apiKeyVerifier {
	return &apiKeyVerifier{
		compare:  crypto.VerifyPassphrase,
		attempts: httprate.NewRateLimiter(apiKeyAttemptsPerMinute, time.Minute),
		slots:    make(chan struct{}, apiKeyVerifySlots),
		slotWait: apiKeySlotWait,
	}
}

// isCached reports whether digest is the last key verified against hash. The
// digest is an in-memory lookup of a random 256-bit key, never persisted; the
// stored credential remains the bcrypt hash.
func (v *apiKeyVerifier) isCached(digest [sha256.Size]byte, hash string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.cachedHash == hash && subtle.ConstantTimeCompare(v.cachedDigest[:], digest[:]) == 1
}

func (v *apiKeyVerifier) check(w http.ResponseWriter, r *http.Request, key, hash string) keyVerdict {
	digest := sha256.Sum256([]byte(key))
	if v.isCached(digest, hash) {
		return keyValid
	}

	timer := time.NewTimer(v.slotWait)
	defer timer.Stop()
	select {
	case v.slots <- struct{}{}:
		defer func() { <-v.slots }()
	case <-timer.C:
		return keyThrottled
	case <-r.Context().Done():
		return keyThrottled
	}

	// Another request may have verified this key while this one queued. Checked
	// before the limiter so one client's cold-start burst isn't charged for it.
	if v.isCached(digest, hash) {
		return keyValid
	}
	client, err := keyByRemoteAddr(r)
	if err != nil || v.attempts.OnLimit(w, r, client) {
		return keyThrottled
	}
	if err := v.compare(key, hash); err != nil {
		return keyInvalid
	}

	v.mu.Lock()
	v.cachedHash, v.cachedDigest = hash, digest
	v.mu.Unlock()
	return keyValid
}

// localHostNames returns the names this machine answers to. An attacker can
// point their own domain at 127.0.0.1 but can't make a browser send these.
func localHostNames() map[string]struct{} {
	names := map[string]struct{}{"localhost": {}}
	if name, err := os.Hostname(); err == nil && name != "" {
		name = strings.ToLower(name)
		names[name] = struct{}{}
		names[name+".local"] = struct{}{}
	}
	return names
}

// isLocalHostHeader reports whether a Host header names this machine: an IP
// literal or one of its own names. This is what defeats DNS rebinding, where
// the browser reaches loopback under an attacker's domain and treats the
// daemon as same-origin.
func isLocalHostHeader(hostHeader string, names map[string]struct{}) bool {
	host, _, err := net.SplitHostPort(hostHeader)
	if err != nil {
		host = hostHeader
	}
	host = strings.Trim(host, "[]")
	if net.ParseIP(host) != nil {
		return true
	}
	_, ok := names[strings.ToLower(strings.TrimSuffix(host, "."))]
	return ok
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

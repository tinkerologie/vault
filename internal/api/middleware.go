package api

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
)

// maxRequestBodySize is the default maximum request body size (1 MB).
const maxRequestBodySize int64 = 1 << 20

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
// Detection uses the Sec-Fetch-Site header which browsers set automatically
// on every request. A value of "same-origin" means the request originated
// from the same origin as the server — i.e., the embedded SPA. This header
// cannot be spoofed by JavaScript or set by non-browser HTTP clients.
func LocalUIBypass(resolve KeyResolver) func(http.Handler) http.Handler {
	apiKeyAuth := APIKeyAuth(resolve)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isTrustedUIRequest(r) {
				next.ServeHTTP(w, r)
				return
			}

			// Not a same-origin browser request — fall through to
			// standard API key authentication.
			apiKeyAuth(next).ServeHTTP(w, r)
		})
	}
}

// AdminBoundary returns middleware that allows same-origin browser requests
// through unconditionally, but enforces API key authentication for all other
// requests — even when no API key is currently configured. This prevents
// arbitrary external clients from calling admin-only endpoints (key rotation,
// key revocation) when authentication has not yet been set up.
//
// Unlike LocalUIBypass, this does NOT fall back to "allow-all" when no key is
// configured, ensuring these security-sensitive endpoints are browser-only
// unless the caller presents a valid key.
func AdminBoundary(resolve KeyResolver) func(http.Handler) http.Handler {
	apiKeyAuth := APIKeyAuth(resolve)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isTrustedUIRequest(r) {
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

// isTrustedUIRequest identifies requests originating from the built-in web UI.
// Browsers typically send Fetch Metadata and Origin/Referer headers for these
// requests, while ordinary third-party API clients do not.
func isTrustedUIRequest(r *http.Request) bool {
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

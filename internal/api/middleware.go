package api

import (
	"crypto/subtle"
	"net/http"
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
			// Browsers always set Sec-Fetch-Site on requests. If the
			// value is "same-origin", the request came from the SPA
			// served by this same server — let it through.
			if r.Header.Get("Sec-Fetch-Site") == "same-origin" {
				next.ServeHTTP(w, r)
				return
			}

			// Not a same-origin browser request — fall through to
			// standard API key authentication.
			apiKeyAuth(next).ServeHTTP(w, r)
		})
	}
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

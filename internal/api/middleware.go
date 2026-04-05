package api

import (
	"log"
	"net"
	"net/http"
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

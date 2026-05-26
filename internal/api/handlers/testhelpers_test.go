package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ruaan-deysel/vault/internal/db"
)

// newTestDB opens a real SQLite DB backed by a temp file and registers a
// cleanup to close it when the test finishes.
func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "vault.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// newReq builds an httptest.Request. When body is nil the request has no body.
func newReq(method, path string, body []byte) *http.Request {
	if body != nil {
		return httptest.NewRequest(method, path, bytes.NewReader(body))
	}
	return httptest.NewRequest(method, path, nil)
}

// withURLParam attaches a single chi URL parameter to a request's context.
func withURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

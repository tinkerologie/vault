package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tinkerologie/vault/internal/replication"
)

// TestReplicationUpdateMissingSourceWithSyncer610 pins the 404 for an update
// of a source that doesn't exist when the syncer is wired, as in the daemon.
func TestReplicationUpdateMissingSourceWithSyncer610(t *testing.T) {
	t.Parallel()
	_, d := setupReplicationTest(t)
	syncer := replication.NewSyncer(d, nil)
	h := NewReplicationHandler(d, func() *replication.Syncer { return syncer }, make([]byte, 32), nil, nil)
	req := httptest.NewRequest(http.MethodPut, "/replication/x", strings.NewReader(`{"schedule":"0 4 * * *"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "424242")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.Update(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d %s, want 404", w.Code, w.Body.String())
	}
}

package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestJobHandler_GetHistory_UnknownJob_404 ensures history for a non-existent
// job is a 404 rather than a misleading empty list.
func TestJobHandler_GetHistory_UnknownJob_404(t *testing.T) {
	h := newJobHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq("GET", "/jobs/99999/history", nil), "id", "99999")
	h.GetHistory(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (%s)", w.Code, w.Body.String())
	}
}

// TestStorageHandler_DependentJobs_UnknownDest_404 ensures querying dependents
// of a non-existent destination is a 404, not a "zero dependents" 200.
func TestStorageHandler_DependentJobs_UnknownDest_404(t *testing.T) {
	h := newStorageHandlerForTest(t)
	w := httptest.NewRecorder()
	h.DependentJobs(w, reqWithID(http.MethodGet, "/api/v1/storage/99999/jobs", "99999", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (%s)", w.Code, w.Body.String())
	}
}

// TestAnomalyHandler_GetBaseline_UnknownJob_404 ensures a baseline request for
// a non-existent job is a 404, while a known-but-learning job still returns a
// zero baseline (covered by TestAnomalyGetBaseline_NoBaseline).
func TestAnomalyHandler_GetBaseline_UnknownJob_404(t *testing.T) {
	d := newTestDB(t)
	h := NewAnomalyHandler(d)
	w := httptest.NewRecorder()
	r := withURLParam(newReq("GET", "/jobs/99999/baseline", nil), "id", "99999")
	h.GetBaseline(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (%s)", w.Code, w.Body.String())
	}
}

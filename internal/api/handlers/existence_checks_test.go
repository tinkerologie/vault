package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandlers_UnknownID_404 verifies that endpoints which previously returned
// a misleading 200 (empty list / zero dependents / zero baseline) now return
// 404 for a non-existent id. A known-but-learning job's baseline still returns
// a 200 zero payload (covered by TestAnomalyGetBaseline_NoBaseline).
func TestHandlers_UnknownID_404(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(t *testing.T) *httptest.ResponseRecorder
	}{
		{
			name: "job history unknown job",
			invoke: func(t *testing.T) *httptest.ResponseRecorder {
				h := newJobHandler(t)
				w := httptest.NewRecorder()
				h.GetHistory(w, withURLParam(newReq(http.MethodGet, "/jobs/99999/history", nil), "id", "99999"))
				return w
			},
		},
		{
			name: "storage dependent jobs unknown destination",
			invoke: func(t *testing.T) *httptest.ResponseRecorder {
				h := newStorageHandlerForTest(t)
				w := httptest.NewRecorder()
				h.DependentJobs(w, reqWithID(http.MethodGet, "/api/v1/storage/99999/jobs", "99999", nil))
				return w
			},
		},
		{
			name: "anomaly baseline unknown job",
			invoke: func(t *testing.T) *httptest.ResponseRecorder {
				h := NewAnomalyHandler(newTestDB(t))
				w := httptest.NewRecorder()
				h.GetBaseline(w, withURLParam(newReq(http.MethodGet, "/jobs/99999/baseline", nil), "id", "99999"))
				return w
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := tt.invoke(t)
			if w.Code != http.StatusNotFound {
				t.Fatalf("got %d, want 404 (%s)", w.Code, w.Body.String())
			}
		})
	}
}

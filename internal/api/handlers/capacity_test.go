package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestHealthSummary_DBClosed forces ListJobs to fail.
func TestHealthSummary_DBClosed(t *testing.T) {
	t.Parallel()
	d := newTestDB(t)
	h := NewHealthHandler(d)
	_ = d.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health/summary", nil)
	w := httptest.NewRecorder()
	h.Summary(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestHistoryPurge_DBClosed forces DeleteOldActivityLogs to fail.
func TestHistoryPurge_DBClosed(t *testing.T) {
	t.Parallel()
	d := newTestDB(t)
	h := NewHistoryHandler(d)
	_ = d.Close()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/history", nil)
	w := httptest.NewRecorder()
	h.Purge(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

// TestStorageResponseWithCapacity_ProbedNilByteFields hits the else-branches
// where ProbedAt is set but the individual byte counters are nil — emitting
// int64(0) defaults instead of dereferencing pointers.
func TestStorageResponseWithCapacity_ProbedNilByteFields(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	d := db.StorageDestination{
		ID:               1,
		Name:             "stub",
		Type:             "local",
		Config:           "{}",
		CapacityProbedAt: &now,
		// CapacityTotalBytes / Used / Free intentionally nil → else branch.
	}
	got := storageResponseWithCapacity(d)
	capObj, ok := got["capacity"].(map[string]any)
	if !ok {
		t.Fatalf("capacity not a map: %T", got["capacity"])
	}
	if capObj["total_bytes"] != int64(0) {
		t.Errorf("total_bytes = %v, want int64(0)", capObj["total_bytes"])
	}
	if capObj["used_bytes"] != int64(0) {
		t.Errorf("used_bytes = %v, want int64(0)", capObj["used_bytes"])
	}
	if capObj["free_bytes"] != int64(0) {
		t.Errorf("free_bytes = %v, want int64(0)", capObj["free_bytes"])
	}
}

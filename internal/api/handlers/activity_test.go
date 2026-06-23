package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

func TestNewActivityHandler(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	if h.db != d {
		t.Error("handler db field not set")
	}
}

func TestActivityList_EmptyDB(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/activity", nil)
	h.List(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp []db.ActivityLogEntry
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(resp))
	}
}

func TestActivityList_WithEntries(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)

	// Seed some entries.
	d.LogActivity("info", "backup", "msg1", "{}")
	d.LogActivity("warn", "system", "msg2", "{}")
	d.LogActivity("info", "backup", "msg3", "{}")

	tests := []struct {
		name    string
		query   string
		wantLen int
	}{
		{
			name:    "no query params returns all (up to default 100)",
			query:   "",
			wantLen: 3,
		},
		{
			name:    "limit=2 returns 2",
			query:   "?limit=2",
			wantLen: 2,
		},
		{
			name:    "category=backup filters",
			query:   "?category=backup",
			wantLen: 2,
		},
		{
			name:    "category=system filters",
			query:   "?category=system",
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := newReq(http.MethodGet, "/api/v1/activity"+tt.query, nil)
			h.List(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
			}
			var resp []db.ActivityLogEntry
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(resp) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(resp), tt.wantLen)
			}
		})
	}
}

func TestActivityList_LimitClampedToMax(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)

	// Seed 3 entries.
	for i := 0; i < 3; i++ {
		d.LogActivity("info", "backup", "msg", "{}")
	}

	w := httptest.NewRecorder()
	// Passing a limit beyond the max should still work (clamped server-side).
	r := newReq(http.MethodGet, "/api/v1/activity?limit=99999", nil)
	h.List(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestActivityList_InvalidLimit(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)
	d.LogActivity("info", "backup", "msg", "{}")

	// A non-numeric limit is rejected with 400, matching /jobs/{id}/history.
	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/activity?limit=abc", nil)
	h.List(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestActivityPurge_Happy(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)

	d.LogActivity("info", "backup", "msg", "{}")
	d.LogActivity("info", "backup", "msg2", "{}")

	w := httptest.NewRecorder()
	r := newReq(http.MethodDelete, "/api/v1/activity", nil)
	h.Purge(w, r)

	// The handler should always return 204 NoContent.
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

func TestActivityPurge_EmptyDB(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)

	w := httptest.NewRecorder()
	r := newReq(http.MethodDelete, "/api/v1/activity", nil)
	h.Purge(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", w.Code, w.Body.String())
	}
}

func TestActivityPurge_DBError(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)

	// Close the DB to trigger an internal error path.
	_ = d.Close()

	w := httptest.NewRecorder()
	r := newReq(http.MethodDelete, "/api/v1/activity", nil)
	h.Purge(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

func TestActivityList_DBError(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)

	// Close the DB to trigger an internal error path.
	_ = d.Close()

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/activity", nil)
	h.List(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
}

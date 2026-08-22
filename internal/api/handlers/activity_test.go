package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

	// Invalid limits are rejected with 400, matching /jobs/{id}/history.
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"non-numeric", "/api/v1/activity?limit=abc", http.StatusBadRequest},
		{"negative", "/api/v1/activity?limit=-5", http.StatusBadRequest},
		{"zero", "/api/v1/activity?limit=0", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := newReq(http.MethodGet, tt.query, nil)
			h.List(w, r)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
		})
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

func TestActivityList_BeforeIDPagesBackwards(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)

	for i := 0; i < 5; i++ {
		d.LogActivity("info", "backup", "msg", "{}")
	}

	get := func(t *testing.T, query string) []db.ActivityLogEntry {
		t.Helper()
		w := httptest.NewRecorder()
		r := newReq(http.MethodGet, "/api/v1/activity"+query, nil)
		h.List(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
		}
		var resp []db.ActivityLogEntry
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}

	// First page is the cursor harness: its last id seeds the backwards walk.
	page1 := get(t, "?limit=2")
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}

	// Each row is one cursor-keyed page fetch and its expected page size.
	pages := []struct {
		name  string
		query string
		want  int
	}{
		{name: "second page walks backwards from the first page's cursor", query: "?limit=2&before_id=" + strconv.FormatInt(page1[1].ID, 10), want: 2},
		{name: "cursor at the oldest seeded id returns an empty page", query: "?before_id=1", want: 0},
	}
	var page2 []db.ActivityLogEntry
	for _, tc := range pages {
		t.Run(tc.name, func(t *testing.T) {
			got := get(t, tc.query)
			if len(got) != tc.want {
				t.Fatalf("%s: len = %d, want %d", tc.name, len(got), tc.want)
			}
			if tc.want > 0 {
				page2 = got
			}
		})
	}

	// Cross-page assertion: the two pages must not overlap and must cover
	// 4 distinct ids.
	seen := make(map[int64]bool)
	for _, e := range append(append([]db.ActivityLogEntry{}, page1...), page2...) {
		if seen[e.ID] {
			t.Errorf("entry %d appeared on both pages", e.ID)
		}
		seen[e.ID] = true
	}
	if len(seen) != 4 {
		t.Errorf("distinct ids across pages = %d, want 4", len(seen))
	}
}

func TestActivityList_BeforeIDWithCategory(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)

	for i := 0; i < 3; i++ {
		d.LogActivity("info", "backup", "msg", "{}")
		d.LogActivity("info", "system", "msg", "{}")
	}

	get := func(t *testing.T, query string) []db.ActivityLogEntry {
		t.Helper()
		w := httptest.NewRecorder()
		r := newReq(http.MethodGet, "/api/v1/activity"+query, nil)
		h.List(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
		}
		var resp []db.ActivityLogEntry
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}

	// First page is the cursor harness: its id seeds the backwards walk.
	first := get(t, "?category=system&limit=1")
	if len(first) != 1 || first[0].Category != "system" {
		t.Fatalf("first page = %+v, want 1 system entry", first)
	}

	// Each row is one category-filtered cursor-keyed page fetch.
	pages := []struct {
		name         string
		query        string
		wantCategory string
	}{
		{name: "second page walks backwards within the category", query: "?category=system&limit=1&before_id=" + strconv.FormatInt(first[0].ID, 10), wantCategory: "system"},
	}
	var second []db.ActivityLogEntry
	for _, tc := range pages {
		t.Run(tc.name, func(t *testing.T) {
			got := get(t, tc.query)
			if len(got) != 1 || got[0].Category != tc.wantCategory {
				t.Fatalf("%s = %+v, want 1 %s entry", tc.name, got, tc.wantCategory)
			}
			second = got
		})
	}

	// Cross-page assertion: the two pages must not overlap.
	if first[0].ID == second[0].ID {
		t.Errorf("pages overlapped at id %d", first[0].ID)
	}
}

func TestActivityList_InvalidBeforeID(t *testing.T) {
	d := newTestDB(t)
	h := NewActivityHandler(d)
	d.LogActivity("info", "backup", "msg", "{}")

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"non-numeric", "/api/v1/activity?before_id=abc", http.StatusBadRequest},
		{"negative", "/api/v1/activity?before_id=-5", http.StatusBadRequest},
		{"zero", "/api/v1/activity?before_id=0", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := newReq(http.MethodGet, tt.query, nil)
			h.List(w, r)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}

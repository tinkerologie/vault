package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/anomaly"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newAnomalyHandler creates an AnomalyHandler backed by a real temp-file DB.
func newAnomalyHandler(t *testing.T) (*AnomalyHandler, *db.DB) {
	t.Helper()
	d := newTestDB(t)
	h := NewAnomalyHandler(d)
	return h, d
}

// newAnomalyHandlerWithEvaluator creates a handler wired to a real evaluator
// (started, with a real hub). Use for ack tests that need WS broadcast.
func newAnomalyHandlerWithEvaluator(t *testing.T) (*AnomalyHandler, *db.DB) {
	t.Helper()
	d := newTestDB(t)
	h := NewAnomalyHandler(d)

	hub := ws.NewHub()
	go hub.Run()
	reg := &anomaly.Registry{}
	ev := anomaly.NewEvaluator(d, hub, reg, anomaly.RealClock{})
	ev.Start()
	h.SetEvaluator(ev)
	return h, d
}

// seedAnomaly inserts a minimal open anomaly and returns its ID.
func seedAnomaly(t *testing.T, d *db.DB) int64 {
	t.Helper()
	now := time.Now()
	a := db.Anomaly{
		Fingerprint: "fp-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Detector:    "test.detector",
		Severity:    "warning",
		ScopeKind:   "job",
		ScopeID:     1,
		Metric:      "bytes",
		Observed:    100,
		Summary:     "test anomaly",
		Details:     "{}",
		State:       "open",
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
	inserted, err := d.InsertOpenAnomaly(a)
	if err != nil || !inserted {
		t.Fatalf("seedAnomaly: inserted=%v err=%v", inserted, err)
	}
	// Retrieve to get the DB-assigned ID.
	row, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		t.Fatalf("seedAnomaly fetch: %v", err)
	}
	return row.ID
}

// seedAnomalyN inserts n anomalies with LastSeenAt spaced 1 second apart and
// returns their IDs (oldest first).
func seedAnomalyN(t *testing.T, d *db.DB, n int) []int64 {
	t.Helper()
	base := time.Now().Add(-time.Duration(n) * time.Second)
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		a := db.Anomaly{
			Fingerprint: "fp-bulk-" + strconv.Itoa(i) + "-" + strconv.FormatInt(ts.UnixNano(), 10),
			Detector:    "test.bulk",
			Severity:    "info",
			ScopeKind:   "job",
			ScopeID:     1,
			Metric:      "bytes",
			Observed:    float64(i),
			Summary:     "bulk anomaly " + strconv.Itoa(i),
			Details:     "{}",
			State:       "open",
			FirstSeenAt: ts,
			LastSeenAt:  ts,
		}
		if _, err := d.InsertOpenAnomaly(a); err != nil {
			t.Fatalf("seedAnomalyN[%d]: %v", i, err)
		}
		row, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
		if err != nil {
			t.Fatalf("seedAnomalyN fetch[%d]: %v", i, err)
		}
		ids = append(ids, row.ID)
	}
	return ids
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestAnomalyList_DefaultFilterReturnsOpenAnomalies(t *testing.T) {
	h, d := newAnomalyHandler(t)
	seedAnomaly(t, d)

	w := httptest.NewRecorder()
	h.List(w, newReq(http.MethodGet, "/api/v1/anomalies", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	anomalies, _ := resp["anomalies"].([]any)
	if len(anomalies) == 0 {
		t.Error("expected at least 1 open anomaly by default")
	}
}

func TestAnomalyList_EmptyWhenNoRows(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	w := httptest.NewRecorder()
	h.List(w, newReq(http.MethodGet, "/api/v1/anomalies", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	anomalies, _ := resp["anomalies"].([]any)
	if len(anomalies) != 0 {
		t.Errorf("expected 0 anomalies, got %d", len(anomalies))
	}
	if resp["next_cursor"] != "" {
		t.Errorf("expected empty next_cursor, got %q", resp["next_cursor"])
	}
}

func TestAnomalyList_FilterBySeverity(t *testing.T) {
	h, d := newAnomalyHandler(t)
	// Seed a warning anomaly.
	seedAnomaly(t, d)

	// Request only critical severity — should return nothing.
	req := newReq(http.MethodGet, "/api/v1/anomalies?severity=critical", nil)
	req.URL.RawQuery = "severity=critical&state=open"
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	anomalies, _ := resp["anomalies"].([]any)
	if len(anomalies) != 0 {
		t.Errorf("expected 0 critical anomalies, got %d", len(anomalies))
	}
}

func TestAnomalyList_FilterByScopeKind(t *testing.T) {
	h, d := newAnomalyHandler(t)
	seedAnomaly(t, d)

	req := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req.URL.RawQuery = "scope_kind=destination&state=open"
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	anomalies, _ := resp["anomalies"].([]any)
	// Seeded anomaly has scope_kind=job, not destination.
	if len(anomalies) != 0 {
		t.Errorf("expected 0 destination anomalies, got %d", len(anomalies))
	}
}

func TestAnomalyList_LimitCappedAt500(t *testing.T) {
	h, _ := newAnomalyHandler(t)

	req := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req.URL.RawQuery = "limit=9999&state=open"
	w := httptest.NewRecorder()
	h.List(w, req)

	// Should not return an error; limit is silently capped.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestAnomalyList_InvalidLimit(t *testing.T) {
	h, _ := newAnomalyHandler(t)

	req := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req.URL.RawQuery = "limit=abc"
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestAnomalyList_InvalidSinceParam(t *testing.T) {
	h, _ := newAnomalyHandler(t)

	req := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req.URL.RawQuery = "since=notadate"
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestAnomalyList_InvalidCursor(t *testing.T) {
	h, _ := newAnomalyHandler(t)

	req := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req.URL.RawQuery = "cursor=garbage-not-a-cursor"
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestAnomalyList_InvalidScopeID(t *testing.T) {
	h, _ := newAnomalyHandler(t)

	req := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req.URL.RawQuery = "scope_id=notanumber"
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestAnomalyList_SinceRelative(t *testing.T) {
	h, d := newAnomalyHandler(t)
	seedAnomaly(t, d)

	req := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req.URL.RawQuery = "since=-7d&state=open"
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestAnomalyList_CursorPagination(t *testing.T) {
	h, d := newAnomalyHandler(t)
	// Seed 5 anomalies; page size = 3 → two pages, no overlap.
	seedAnomalyN(t, d, 5)

	// Page 1: limit=3.
	req1 := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req1.URL.RawQuery = "limit=3&state=open&since=-1d"
	w1 := httptest.NewRecorder()
	h.List(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("page1 status = %d; body: %s", w1.Code, w1.Body.String())
	}
	var resp1 map[string]any
	if err := json.Unmarshal(w1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("page1 decode: %v", err)
	}
	page1Items, _ := resp1["anomalies"].([]any)
	nextCursor, _ := resp1["next_cursor"].(string)
	if len(page1Items) != 3 {
		t.Fatalf("page1 want 3 items, got %d", len(page1Items))
	}
	if nextCursor == "" {
		t.Fatal("expected non-empty next_cursor after page1")
	}

	// Collect page1 IDs.
	page1IDs := make(map[float64]bool)
	for _, item := range page1Items {
		m, _ := item.(map[string]any)
		page1IDs[m["id"].(float64)] = true
	}

	// Page 2: follow the cursor.
	req2 := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req2.URL.RawQuery = "limit=3&state=open&since=-1d&cursor=" + nextCursor
	w2 := httptest.NewRecorder()
	h.List(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("page2 status = %d; body: %s", w2.Code, w2.Body.String())
	}
	var resp2 map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("page2 decode: %v", err)
	}
	page2Items, _ := resp2["anomalies"].([]any)
	if len(page2Items) == 0 {
		t.Fatal("page2 should have at least 1 item")
	}

	// Assert no overlap between page1 and page2.
	for _, item := range page2Items {
		m, _ := item.(map[string]any)
		if page1IDs[m["id"].(float64)] {
			t.Errorf("anomaly id=%.0f appears in both pages (overlap)", m["id"].(float64))
		}
	}

	// Total coverage: page1 + page2 must equal 5 seeded rows.
	if len(page1Items)+len(page2Items) != 5 {
		t.Errorf("total items = %d, want 5", len(page1Items)+len(page2Items))
	}
}

func TestAnomalyList_NextCursorAbsentWhenLastPage(t *testing.T) {
	h, d := newAnomalyHandler(t)
	seedAnomalyN(t, d, 2)

	req := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req.URL.RawQuery = "limit=10&state=open&since=-1d"
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Only 2 rows, limit=10 → next_cursor must be empty.
	if next := resp["next_cursor"]; next != "" {
		t.Errorf("expected empty next_cursor on last page, got %q", next)
	}
}

func TestAnomalyList_MultiValueState(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	req := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req.URL.RawQuery = "state=open&state=resolved"
	w := httptest.NewRecorder()
	h.List(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestAnomalyList_CSVState(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	req := newReq(http.MethodGet, "/api/v1/anomalies", nil)
	req.URL.RawQuery = "state=open,resolved"
	w := httptest.NewRecorder()
	h.List(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestAnomalyGet_Found(t *testing.T) {
	h, d := newAnomalyHandler(t)
	id := seedAnomaly(t, d)

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/anomalies/"+strconv.FormatInt(id, 10), nil), "id", strconv.FormatInt(id, 10))
	h.Get(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["id"] == nil {
		t.Error("response missing 'id' field")
	}
}

func TestAnomalyGet_NotFound(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/anomalies/9999", nil), "id", "9999")
	h.Get(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestAnomalyGet_InvalidID(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/anomalies/abc", nil), "id", "abc")
	h.Get(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Ack
// ---------------------------------------------------------------------------

func TestAnomalyAck_ValidDismiss(t *testing.T) {
	h, d := newAnomalyHandlerWithEvaluator(t)
	id := seedAnomaly(t, d)

	body, _ := json.Marshal(map[string]any{
		"action": "dismiss",
		"reason": "expected",
		"by":     "test-user",
	})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/anomalies/"+strconv.FormatInt(id, 10)+"/ack", body), "id", strconv.FormatInt(id, 10))
	h.Ack(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// State should be "acknowledged" after dismiss.
	if state, _ := resp["state"].(string); state != "acknowledged" {
		t.Errorf("state = %q, want 'acknowledged'", state)
	}
}

func TestAnomalyAck_ValidMarkExpected(t *testing.T) {
	h, d := newAnomalyHandler(t) // use db-fallback path
	id := seedAnomaly(t, d)

	body, _ := json.Marshal(map[string]any{
		"action": "mark_expected",
		"reason": "this is fine",
	})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/anomalies/"+strconv.FormatInt(id, 10)+"/ack", body), "id", strconv.FormatInt(id, 10))
	h.Ack(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	// Verify DB state changed.
	updated, err := d.GetAnomaly(id)
	if err != nil {
		t.Fatalf("get updated anomaly: %v", err)
	}
	if updated.State != "expected" {
		t.Errorf("state = %q, want 'expected'", updated.State)
	}
}

func TestAnomalyAck_ByDefaultsToAPI(t *testing.T) {
	h, d := newAnomalyHandler(t)
	id := seedAnomaly(t, d)

	body, _ := json.Marshal(map[string]any{
		"action": "dismiss",
		// by is omitted → should default to "api"
	})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/anomalies/"+strconv.FormatInt(id, 10)+"/ack", body), "id", strconv.FormatInt(id, 10))
	h.Ack(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	updated, err := d.GetAnomaly(id)
	if err != nil {
		t.Fatalf("get updated: %v", err)
	}
	if updated.AckBy != "api" {
		t.Errorf("ack_by = %q, want 'api'", updated.AckBy)
	}
}

func TestAnomalyAck_InvalidAction(t *testing.T) {
	h, d := newAnomalyHandler(t)
	id := seedAnomaly(t, d)

	body, _ := json.Marshal(map[string]any{"action": "delete_all"})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/anomalies/"+strconv.FormatInt(id, 10)+"/ack", body), "id", strconv.FormatInt(id, 10))
	h.Ack(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", w.Code, w.Body.String())
	}
}

func TestAnomalyAck_ReasonTooLong(t *testing.T) {
	h, d := newAnomalyHandler(t)
	id := seedAnomaly(t, d)

	longReason := make([]byte, 501)
	for i := range longReason {
		longReason[i] = 'x'
	}
	body, _ := json.Marshal(map[string]any{
		"action": "dismiss",
		"reason": string(longReason),
	})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/anomalies/"+strconv.FormatInt(id, 10)+"/ack", body), "id", strconv.FormatInt(id, 10))
	h.Ack(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", w.Code, w.Body.String())
	}
}

func TestAnomalyAck_AlreadyTerminal_Returns409(t *testing.T) {
	h, d := newAnomalyHandler(t)
	id := seedAnomaly(t, d)

	// First ack succeeds.
	body, _ := json.Marshal(map[string]any{"action": "dismiss"})
	r1 := withURLParam(newReq(http.MethodPost, "/api/v1/anomalies/"+strconv.FormatInt(id, 10)+"/ack", body), "id", strconv.FormatInt(id, 10))
	w1 := httptest.NewRecorder()
	h.Ack(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first ack status = %d; body: %s", w1.Code, w1.Body.String())
	}

	// Second ack must return 409 (already terminal).
	body2, _ := json.Marshal(map[string]any{"action": "dismiss"})
	r2 := withURLParam(newReq(http.MethodPost, "/api/v1/anomalies/"+strconv.FormatInt(id, 10)+"/ack", body2), "id", strconv.FormatInt(id, 10))
	w2 := httptest.NewRecorder()
	h.Ack(w2, r2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("second ack status = %d, want 409; body: %s", w2.Code, w2.Body.String())
	}
}

func TestAnomalyAck_MissingID_Returns404(t *testing.T) {
	h, _ := newAnomalyHandler(t)

	body, _ := json.Marshal(map[string]any{"action": "dismiss"})
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/anomalies/9999/ack", body), "id", "9999")
	h.Ack(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestAnomalyAck_InvalidJSON(t *testing.T) {
	h, d := newAnomalyHandler(t)
	id := seedAnomaly(t, d)

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodPost, "/api/v1/anomalies/"+strconv.FormatInt(id, 10)+"/ack", []byte("!bad")), "id", strconv.FormatInt(id, 10))
	h.Ack(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// AckBulk
// ---------------------------------------------------------------------------

func TestAnomalyAckBulk_MixedIDs(t *testing.T) {
	h, d := newAnomalyHandler(t)
	ids := seedAnomalyN(t, d, 3)

	// Dismiss 2 open + 1 already-terminal (the third will be dismissed first).
	_, err := d.AckAnomaly(ids[2], "dismiss", "api", "", time.Now())
	if err != nil {
		t.Fatalf("pre-ack: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"ids":    ids,
		"action": "dismiss",
		"reason": "bulk test",
		"by":     "test",
	})
	w := httptest.NewRecorder()
	h.AckBulk(w, newReq(http.MethodPost, "/api/v1/anomalies/ack-bulk", body))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 2 open → acknowledged, 1 already terminal → skipped.
	if resp["acknowledged"].(float64) != 2 {
		t.Errorf("acknowledged = %.0f, want 2", resp["acknowledged"].(float64))
	}
	if resp["skipped"].(float64) != 1 {
		t.Errorf("skipped = %.0f, want 1", resp["skipped"].(float64))
	}
}

func TestAnomalyAckBulk_EmptyIDs(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	body, _ := json.Marshal(map[string]any{
		"ids":    []int64{},
		"action": "dismiss",
	})
	w := httptest.NewRecorder()
	h.AckBulk(w, newReq(http.MethodPost, "/api/v1/anomalies/ack-bulk", body))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["acknowledged"].(float64) != 0 {
		t.Errorf("acknowledged = %.0f, want 0", resp["acknowledged"].(float64))
	}
}

func TestAnomalyAckBulk_InvalidAction(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	body, _ := json.Marshal(map[string]any{
		"ids":    []int64{1},
		"action": "nuke",
	})
	w := httptest.NewRecorder()
	h.AckBulk(w, newReq(http.MethodPost, "/api/v1/anomalies/ack-bulk", body))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", w.Code, w.Body.String())
	}
}

func TestAnomalyAckBulk_ReasonTooLong(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	longReason := make([]byte, 501)
	for i := range longReason {
		longReason[i] = 'y'
	}
	body, _ := json.Marshal(map[string]any{
		"ids":    []int64{1},
		"action": "dismiss",
		"reason": string(longReason),
	})
	w := httptest.NewRecorder()
	h.AckBulk(w, newReq(http.MethodPost, "/api/v1/anomalies/ack-bulk", body))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", w.Code, w.Body.String())
	}
}

func TestAnomalyAckBulk_InvalidJSON(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	w := httptest.NewRecorder()
	h.AckBulk(w, newReq(http.MethodPost, "/api/v1/anomalies/ack-bulk", []byte("!bad")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// GetBaseline
// ---------------------------------------------------------------------------

func TestAnomalyGetBaseline_NoBaseline(t *testing.T) {
	h, d := newAnomalyHandler(t)
	jobID := seedJob(t, d)

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(jobID, 10)+"/baseline", nil), "id", strconv.FormatInt(jobID, 10))
	h.GetBaseline(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestAnomalyGetBaseline_Found(t *testing.T) {
	h, d := newAnomalyHandler(t)
	jobID := seedJob(t, d)

	now := time.Now()
	if err := d.UpsertJobBaseline(db.JobBaseline{
		JobID:          jobID,
		SampleCount:    5,
		BytesMedian:    1024,
		BytesMAD:       100,
		DurationMedian: 60,
		DurationMAD:    5,
		FailureRate:    0.1,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("upsert baseline: %v", err)
	}

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/"+strconv.FormatInt(jobID, 10)+"/baseline", nil), "id", strconv.FormatInt(jobID, 10))
	h.GetBaseline(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["job_id"] == nil {
		t.Error("response missing 'job_id'")
	}
}

func TestAnomalyGetBaseline_InvalidID(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/jobs/abc/baseline", nil), "id", "abc")
	h.GetBaseline(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// GetTrajectory
// ---------------------------------------------------------------------------

func TestAnomalyGetTrajectory_EmptySamples(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/destinations/1/capacity-trajectory", nil), "id", "1")
	h.GetTrajectory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	samples, _ := resp["samples"].([]any)
	if len(samples) != 0 {
		t.Errorf("expected empty samples, got %d", len(samples))
	}
}

func TestAnomalyGetTrajectory_WithSamples(t *testing.T) {
	h, d := newAnomalyHandler(t)
	destID := seedStorageDest(t, d)

	// Insert two capacity samples.
	now := time.Now()
	for i := 0; i < 2; i++ {
		if err := d.InsertCapacitySample(db.CapacitySample{
			DestID:     destID,
			SampledAt:  now.Add(-time.Duration(i) * time.Hour),
			FreeBytes:  int64(1000 - i*100),
			TotalBytes: 2000,
		}); err != nil {
			t.Fatalf("insert sample %d: %v", i, err)
		}
	}

	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/destinations/"+strconv.FormatInt(destID, 10)+"/capacity-trajectory", nil), "id", strconv.FormatInt(destID, 10))
	h.GetTrajectory(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	samples, _ := resp["samples"].([]any)
	if len(samples) != 2 {
		t.Errorf("expected 2 samples, got %d", len(samples))
	}
}

func TestAnomalyGetTrajectory_InvalidID(t *testing.T) {
	h, _ := newAnomalyHandler(t)
	w := httptest.NewRecorder()
	r := withURLParam(newReq(http.MethodGet, "/api/v1/destinations/xyz/capacity-trajectory", nil), "id", "xyz")
	h.GetTrajectory(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func TestParseMultiParam_CSV(t *testing.T) {
	got := parseMultiParam([]string{"open,resolved", "expected"})
	want := map[string]bool{"open": true, "resolved": true, "expected": true}
	if len(got) != 3 {
		t.Fatalf("want 3 items, got %d: %v", len(got), got)
	}
	for _, v := range got {
		if !want[v] {
			t.Errorf("unexpected value %q", v)
		}
	}
}

func TestParseMultiParam_Dedup(t *testing.T) {
	got := parseMultiParam([]string{"open", "open"})
	if len(got) != 1 {
		t.Errorf("want 1 (deduped), got %d: %v", len(got), got)
	}
}

func TestParseSince_RFC3339(t *testing.T) {
	ts := "2026-01-01T00:00:00Z"
	got, err := parseSince(ts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IsZero() {
		t.Error("expected non-zero time")
	}
}

func TestParseSince_RelativeDay(t *testing.T) {
	got, err := parseSince("-7d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if time.Since(got) < 6*24*time.Hour || time.Since(got) > 8*24*time.Hour {
		t.Errorf("expected ~7 days ago, got %v", got)
	}
}

func TestParseSince_Invalid(t *testing.T) {
	if _, err := parseSince("notadate"); err == nil {
		t.Error("expected error for invalid since value")
	}
}

func TestValidateAckAction_Valid(t *testing.T) {
	for _, action := range []string{"dismiss", "mark_expected"} {
		if _, err := validateAckAction(action); err != nil {
			t.Errorf("action %q should be valid but got error: %v", action, err)
		}
	}
}

func TestValidateAckAction_Invalid(t *testing.T) {
	if _, err := validateAckAction("delete"); err == nil {
		t.Error("expected error for invalid action")
	}
}

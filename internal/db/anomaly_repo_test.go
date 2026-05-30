package db

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

// makeAnomaly returns a minimal valid Anomaly for insertion.
// Callers may override any field before calling InsertOpenAnomaly.
func makeAnomaly(fingerprint string) Anomaly {
	now := time.Now().UTC().Truncate(time.Second)
	return Anomaly{
		Fingerprint: fingerprint,
		Detector:    "test_detector",
		Severity:    "warning",
		ScopeKind:   "job",
		ScopeID:     1,
		Metric:      "bytes",
		Observed:    100.0,
		Summary:     "test summary",
		Details:     "test details",
		State:       "open",
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
}

// TestAnomalyRepoInsertOpenIdempotent verifies that a second InsertOpenAnomaly
// with the same fingerprint while one open row exists returns inserted=false,
// and that after the first row is resolved a fresh insert with the same
// fingerprint succeeds (inserted=true), because the partial unique index only
// constrains rows WHERE state='open'.
func TestAnomalyRepoInsertOpenIdempotent(t *testing.T) {
	d := setupTestDB(t)

	a := makeAnomaly("fp-idempotent")

	// First insert must succeed.
	inserted, err := d.InsertOpenAnomaly(a)
	if err != nil {
		t.Fatalf("first insert error = %v", err)
	}
	if !inserted {
		t.Fatal("first insert: want inserted=true, got false")
	}

	// Second insert with same fingerprint while first is still open → conflict.
	inserted2, err := d.InsertOpenAnomaly(a)
	if err != nil {
		t.Fatalf("second insert (conflict) error = %v", err)
	}
	if inserted2 {
		t.Fatal("second insert while open: want inserted=false, got true")
	}

	// Fetch the open row so we can resolve it.
	open, err := d.GetOpenAnomalyByFingerprint("fp-idempotent")
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}

	// Resolve it — use a non-existent run ID; ResolveOpenAnomaliesForRun
	// only filters on job_run_id.  Insert with a run ID so the method works.
	runID := int64(999)
	a2 := a
	a2.JobRunID = &runID
	a2.Severity = "info"
	insertedWithRunID, err := d.InsertOpenAnomaly(a2)
	if err != nil {
		t.Fatalf("insert with run ID error = %v", err)
	}
	// The first open row is still there for fp-idempotent, so this must conflict.
	if insertedWithRunID {
		t.Fatal("insert with run ID while first open: want false, got true")
	}

	// Resolve the open row directly via AckAnomaly so the partial index is freed.
	acked, err := d.AckAnomaly(open.ID, "dismiss", "tester", "test", time.Now())
	if err != nil {
		t.Fatalf("AckAnomaly: %v", err)
	}
	if !acked {
		t.Fatal("AckAnomaly: want acked=true")
	}

	// Now insert with the same fingerprint — should succeed.
	a3 := makeAnomaly("fp-idempotent")
	inserted3, err := d.InsertOpenAnomaly(a3)
	if err != nil {
		t.Fatalf("insert after ack error = %v", err)
	}
	if !inserted3 {
		t.Fatal("insert after ack: want inserted=true, got false")
	}
}

// TestAnomalyRepoInsertAndList verifies insert → list returns the row.
func TestAnomalyRepoInsertAndList(t *testing.T) {
	d := setupTestDB(t)

	a := makeAnomaly("fp-list-basic")
	inserted, err := d.InsertOpenAnomaly(a)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if !inserted {
		t.Fatal("want inserted=true")
	}

	rows, err := d.ListAnomalies(AnomalyFilter{})
	if err != nil {
		t.Fatalf("ListAnomalies: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("ListAnomalies returned 0 rows, want ≥1")
	}
	found := false
	for _, r := range rows {
		if r.Fingerprint == "fp-list-basic" {
			found = true
			if r.State != "open" {
				t.Errorf("state = %q, want open", r.State)
			}
			if r.Summary != "test summary" {
				t.Errorf("summary = %q, want 'test summary'", r.Summary)
			}
		}
	}
	if !found {
		t.Error("inserted anomaly not found in ListAnomalies result")
	}
}

// TestAnomalyRepoGetAnomaly covers the GetAnomaly by-ID and ErrNotFound path.
func TestAnomalyRepoGetAnomaly(t *testing.T) {
	d := setupTestDB(t)

	a := makeAnomaly("fp-get")
	_, err := d.InsertOpenAnomaly(a)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	open, err := d.GetOpenAnomalyByFingerprint("fp-get")
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}

	got, err := d.GetAnomaly(open.ID)
	if err != nil {
		t.Fatalf("GetAnomaly: %v", err)
	}
	if got.Fingerprint != "fp-get" {
		t.Errorf("Fingerprint = %q, want fp-get", got.Fingerprint)
	}

	// Non-existent ID → ErrNotFound.
	_, err = d.GetAnomaly(99999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAnomaly(99999) error = %v, want ErrNotFound", err)
	}
}

// TestAnomalyRepoRefreshOpenAnomaly verifies RefreshOpenAnomaly updates
// last_seen_at, deviation, and severity, and leaves the rest unchanged.
func TestAnomalyRepoRefreshOpenAnomaly(t *testing.T) {
	d := setupTestDB(t)

	a := makeAnomaly("fp-refresh")
	_, err := d.InsertOpenAnomaly(a)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	open, err := d.GetOpenAnomalyByFingerprint("fp-refresh")
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}

	newSeen := open.LastSeenAt.Add(5 * time.Minute)
	dev := 3.14
	err = d.RefreshOpenAnomaly(open.ID, 200.0, &dev, newSeen, "critical")
	if err != nil {
		t.Fatalf("RefreshOpenAnomaly: %v", err)
	}

	refreshed, err := d.GetAnomaly(open.ID)
	if err != nil {
		t.Fatalf("GetAnomaly after refresh: %v", err)
	}
	if refreshed.Observed != 200.0 {
		t.Errorf("Observed = %v, want 200.0", refreshed.Observed)
	}
	if refreshed.Severity != "critical" {
		t.Errorf("Severity = %q, want critical", refreshed.Severity)
	}
	if refreshed.Deviation == nil || *refreshed.Deviation != 3.14 {
		t.Errorf("Deviation = %v, want &3.14", refreshed.Deviation)
	}
	if !refreshed.LastSeenAt.Equal(newSeen) {
		t.Errorf("LastSeenAt = %v, want %v", refreshed.LastSeenAt, newSeen)
	}
	// FirstSeenAt must be unchanged.
	if !refreshed.FirstSeenAt.Equal(open.FirstSeenAt) {
		t.Errorf("FirstSeenAt changed: got %v, want %v", refreshed.FirstSeenAt, open.FirstSeenAt)
	}
}

// TestAnomalyRepoGetOpenAnomalyByFingerprint verifies the method returns the
// open row and returns ErrNotFound when none exists.
func TestAnomalyRepoGetOpenAnomalyByFingerprint(t *testing.T) {
	d := setupTestDB(t)

	// Missing → ErrNotFound.
	_, err := d.GetOpenAnomalyByFingerprint("fp-missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("missing fingerprint: want ErrNotFound, got %v", err)
	}

	// After insert → returns the row.
	a := makeAnomaly("fp-open-lookup")
	_, err = d.InsertOpenAnomaly(a)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := d.GetOpenAnomalyByFingerprint("fp-open-lookup")
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}
	if got.State != "open" {
		t.Errorf("State = %q, want open", got.State)
	}
}

// TestAnomalyRepoResolveOpenAnomaliesForRun verifies that only open rows
// with the matching run ID AND matching severity are resolved, and that
// open rows with different severities are left untouched.
func TestAnomalyRepoResolveOpenAnomaliesForRun(t *testing.T) {
	d := setupTestDB(t)

	runID := int64(42)
	otherRunID := int64(99)

	// Row 1: severity=warning, runID=42 → should be resolved.
	a1 := makeAnomaly("fp-resolve-1")
	a1.Severity = "warning"
	a1.JobRunID = &runID
	_, err := d.InsertOpenAnomaly(a1)
	if err != nil {
		t.Fatalf("insert a1: %v", err)
	}

	// Row 2: severity=critical, runID=42 → should NOT be resolved (not in list).
	a2 := makeAnomaly("fp-resolve-2")
	a2.Severity = "critical"
	a2.JobRunID = &runID
	_, err = d.InsertOpenAnomaly(a2)
	if err != nil {
		t.Fatalf("insert a2: %v", err)
	}

	// Row 3: severity=info, runID=other → should NOT be resolved (wrong run).
	a3 := makeAnomaly("fp-resolve-3")
	a3.Severity = "info"
	a3.JobRunID = &otherRunID
	_, err = d.InsertOpenAnomaly(a3)
	if err != nil {
		t.Fatalf("insert a3: %v", err)
	}

	resolvedAt := time.Now().UTC()
	n, err := d.ResolveOpenAnomaliesForRun(runID, []string{"warning", "info"}, resolvedAt)
	if err != nil {
		t.Fatalf("ResolveOpenAnomaliesForRun: %v", err)
	}
	if n != 1 {
		t.Errorf("resolved count = %d, want 1 (only a1 matches run+severity)", n)
	}

	// a1 → resolved.
	r1, err := d.GetOpenAnomalyByFingerprint("fp-resolve-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("fp-resolve-1 should not be open any more; got row %+v, err %v", r1, err)
	}

	// a2 → still open (severity not in list).
	r2, err := d.GetOpenAnomalyByFingerprint("fp-resolve-2")
	if err != nil {
		t.Errorf("fp-resolve-2 should still be open: %v", err)
	} else if r2.State != "open" {
		t.Errorf("fp-resolve-2 state = %q, want open", r2.State)
	}

	// a3 → still open (different run ID).
	r3, err := d.GetOpenAnomalyByFingerprint("fp-resolve-3")
	if err != nil {
		t.Errorf("fp-resolve-3 should still be open: %v", err)
	} else if r3.State != "open" {
		t.Errorf("fp-resolve-3 state = %q, want open", r3.State)
	}

	// Empty severities → 0 rows, no error.
	n2, err := d.ResolveOpenAnomaliesForRun(runID, nil, resolvedAt)
	if err != nil {
		t.Fatalf("ResolveOpenAnomaliesForRun(empty) error = %v", err)
	}
	if n2 != 0 {
		t.Errorf("expected 0 rows for empty severities, got %d", n2)
	}
}

// TestAnomalyRepoAckAnomaly verifies state transitions for AckAnomaly.
func TestAnomalyRepoAckAnomaly(t *testing.T) {
	d := setupTestDB(t)

	a := makeAnomaly("fp-ack")
	_, err := d.InsertOpenAnomaly(a)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	open, err := d.GetOpenAnomalyByFingerprint("fp-ack")
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}

	ackedAt := time.Now().UTC().Truncate(time.Second)

	// AckAnomaly on open row → acked=true, state='acknowledged'.
	acked, err := d.AckAnomaly(open.ID, "dismiss", "bob", "not a real issue", ackedAt)
	if err != nil {
		t.Fatalf("AckAnomaly: %v", err)
	}
	if !acked {
		t.Fatal("AckAnomaly: want acked=true on open row")
	}

	got, err := d.GetAnomaly(open.ID)
	if err != nil {
		t.Fatalf("GetAnomaly after ack: %v", err)
	}
	if got.State != "acknowledged" {
		t.Errorf("state = %q, want acknowledged", got.State)
	}
	if got.AckAction != "dismiss" {
		t.Errorf("ack_action = %q, want dismiss", got.AckAction)
	}
	if got.AckBy != "bob" {
		t.Errorf("ack_by = %q, want bob", got.AckBy)
	}
	if got.AckReason != "not a real issue" {
		t.Errorf("ack_reason = %q, want 'not a real issue'", got.AckReason)
	}
	if got.AcknowledgedAt == nil {
		t.Error("acknowledged_at is nil, want non-nil")
	} else if !got.AcknowledgedAt.Equal(ackedAt) {
		// Guards against a regression where acknowledged_at is set to
		// time.Now() instead of the caller-supplied timestamp.
		t.Errorf("acknowledged_at = %v, want caller-supplied %v", got.AcknowledgedAt, ackedAt)
	}

	// AckAnomaly on already-terminal row → acked=false (no error).
	acked2, err := d.AckAnomaly(open.ID, "dismiss", "bob", "again", ackedAt)
	if err != nil {
		t.Fatalf("AckAnomaly on terminal: %v", err)
	}
	if acked2 {
		t.Fatal("AckAnomaly on already-terminal row: want acked=false")
	}
}

// TestAnomalyRepoAckAnomalyMarkExpected verifies that action="mark_expected"
// transitions the state to 'expected' (not 'acknowledged').
func TestAnomalyRepoAckAnomalyMarkExpected(t *testing.T) {
	d := setupTestDB(t)

	a := makeAnomaly("fp-expected")
	_, err := d.InsertOpenAnomaly(a)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	open, err := d.GetOpenAnomalyByFingerprint("fp-expected")
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}

	acked, err := d.AckAnomaly(open.ID, "mark_expected", "alice", "normal fluctuation", time.Now())
	if err != nil {
		t.Fatalf("AckAnomaly mark_expected: %v", err)
	}
	if !acked {
		t.Fatal("want acked=true")
	}

	got, err := d.GetAnomaly(open.ID)
	if err != nil {
		t.Fatalf("GetAnomaly: %v", err)
	}
	if got.State != "expected" {
		t.Errorf("state = %q, want expected", got.State)
	}
}

// TestAnomalyRepoBulkAckAnomalies verifies acknowledged and skipped counts
// are correct for a mixed batch of open and terminal rows.
func TestAnomalyRepoBulkAckAnomalies(t *testing.T) {
	d := setupTestDB(t)

	// Insert 3 open anomalies.
	fps := []string{"fp-bulk-1", "fp-bulk-2", "fp-bulk-3"}
	ids := make([]int64, len(fps))
	for i, fp := range fps {
		a := makeAnomaly(fp)
		_, err := d.InsertOpenAnomaly(a)
		if err != nil {
			t.Fatalf("insert %s: %v", fp, err)
		}
		open, err := d.GetOpenAnomalyByFingerprint(fp)
		if err != nil {
			t.Fatalf("GetOpenAnomalyByFingerprint %s: %v", fp, err)
		}
		ids[i] = open.ID
	}

	// Pre-ack one of them manually.
	_, err := d.AckAnomaly(ids[2], "dismiss", "system", "pre-acked", time.Now())
	if err != nil {
		t.Fatalf("pre-ack: %v", err)
	}

	// BulkAck all three: 2 open, 1 already terminal.
	acked, skipped, err := d.BulkAckAnomalies(ids, "dismiss", "admin", "bulk close", time.Now())
	if err != nil {
		t.Fatalf("BulkAckAnomalies: %v", err)
	}
	if acked != 2 {
		t.Errorf("acknowledged = %d, want 2", acked)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
}

// TestAnomalyRepoListKeysetPagination inserts 5 anomalies and pages through
// them 2-at-a-time, asserting no overlap and full coverage.
func TestAnomalyRepoListKeysetPagination(t *testing.T) {
	d := setupTestDB(t)

	// Insert 5 anomalies with different last_seen_at timestamps so ordering is deterministic.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	total := 5
	for i := 0; i < total; i++ {
		a := makeAnomaly("fp-page-" + strconv.Itoa(i))
		a.LastSeenAt = base.Add(time.Duration(i) * time.Minute)
		a.FirstSeenAt = a.LastSeenAt
		_, err := d.InsertOpenAnomaly(a)
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	seen := make(map[int64]bool)
	cursor := ""
	pages := 0

	for {
		filter := AnomalyFilter{Limit: 2, Cursor: cursor}
		rows, err := d.ListAnomalies(filter)
		if err != nil {
			t.Fatalf("ListAnomalies page %d: %v", pages, err)
		}
		if len(rows) == 0 {
			break
		}
		pages++
		for _, r := range rows {
			if seen[r.ID] {
				t.Errorf("duplicate row ID %d on page %d", r.ID, pages)
			}
			seen[r.ID] = true
		}
		last := rows[len(rows)-1]
		cursor = EncodeAnomalyCursor(last.LastSeenAt, last.ID)
	}

	if len(seen) != total {
		t.Errorf("saw %d unique rows across all pages, want %d", len(seen), total)
	}
	// With 5 rows and limit=2, expect 3 pages (2+2+1).
	if pages != 3 {
		t.Errorf("pages = %d, want 3", pages)
	}
}

// TestAnomalyRepoListFilters verifies States, Severities, ScopeKind, ScopeID,
// and Since filters work correctly.
func TestAnomalyRepoListFilters(t *testing.T) {
	d := setupTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)

	// Insert anomalies with varying attributes.
	insertAnomaly := func(fp, severity, scopeKind string, scopeID int64, seenAt time.Time) int64 {
		a := makeAnomaly(fp)
		a.Severity = severity
		a.ScopeKind = scopeKind
		a.ScopeID = scopeID
		a.LastSeenAt = seenAt
		a.FirstSeenAt = seenAt
		_, err := d.InsertOpenAnomaly(a)
		if err != nil {
			t.Fatalf("insert %s: %v", fp, err)
		}
		open, err := d.GetOpenAnomalyByFingerprint(fp)
		if err != nil {
			t.Fatalf("GetOpenAnomalyByFingerprint %s: %v", fp, err)
		}
		return open.ID
	}

	id1 := insertAnomaly("fp-f1", "warning", "job", 1, now.Add(-2*time.Hour))
	id2 := insertAnomaly("fp-f2", "critical", "job", 1, now.Add(-1*time.Hour))
	insertAnomaly("fp-f3", "info", "dest", 2, now)

	// Filter by States (open only — all should be open).
	rows, err := d.ListAnomalies(AnomalyFilter{States: []string{"open"}})
	if err != nil {
		t.Fatalf("list by States: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("filter States=['open']: got %d rows, want 3", len(rows))
	}

	// Filter by Severities.
	rows, err = d.ListAnomalies(AnomalyFilter{Severities: []string{"warning", "critical"}})
	if err != nil {
		t.Fatalf("list by Severities: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("filter Severities=[warning,critical]: got %d rows, want 2", len(rows))
	}

	// Filter by ScopeKind.
	rows, err = d.ListAnomalies(AnomalyFilter{ScopeKind: "dest"})
	if err != nil {
		t.Fatalf("list by ScopeKind: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("filter ScopeKind=dest: got %d rows, want 1", len(rows))
	}

	// Filter by ScopeID.
	scopeID := int64(1)
	rows, err = d.ListAnomalies(AnomalyFilter{ScopeID: &scopeID})
	if err != nil {
		t.Fatalf("list by ScopeID: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("filter ScopeID=1: got %d rows, want 2", len(rows))
	}

	// Filter by Since: only rows seen at or after now-1h.
	since := now.Add(-1 * time.Hour)
	rows, err = d.ListAnomalies(AnomalyFilter{Since: &since})
	if err != nil {
		t.Fatalf("list by Since: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("filter Since=now-1h: got %d rows, want 2 (fp-f2 and fp-f3)", len(rows))
	}
	// Neither id1 (seen 2h ago) should appear.
	for _, r := range rows {
		if r.ID == id1 {
			t.Error("Since filter: fp-f1 (seen 2h ago) should not appear")
		}
	}
	_ = id2 // used implicitly via count assertion
}

// TestAnomalyRepoCursorRoundTrip verifies EncodeAnomalyCursor →
// DecodeAnomalyCursor round-trips correctly, and that a malformed cursor
// returns an error.
func TestAnomalyRepoCursorRoundTrip(t *testing.T) {
	ts := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	id := int64(42)

	encoded := EncodeAnomalyCursor(ts, id)

	decodedTs, decodedID, err := DecodeAnomalyCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeAnomalyCursor: %v", err)
	}
	if !decodedTs.Equal(ts) {
		t.Errorf("decoded time = %v, want %v", decodedTs, ts)
	}
	if decodedID != id {
		t.Errorf("decoded id = %d, want %d", decodedID, id)
	}

	// Malformed: no colon separator.
	_, _, err = DecodeAnomalyCursor("nocolon")
	if err == nil {
		t.Error("DecodeAnomalyCursor(malformed): want error, got nil")
	}

	// Malformed: non-integer timestamp.
	_, _, err = DecodeAnomalyCursor("abc:42")
	if err == nil {
		t.Error("DecodeAnomalyCursor(bad timestamp): want error, got nil")
	}

	// Malformed: non-integer ID.
	_, _, err = DecodeAnomalyCursor("1234567890:xyz")
	if err == nil {
		t.Error("DecodeAnomalyCursor(bad id): want error, got nil")
	}
}

// TestAnomalyRepoExpectedFloor verifies that ExpectedFloor returns
// MAX(observed) for 'expected' rows, and 0 when none exist.
func TestAnomalyRepoExpectedFloor(t *testing.T) {
	d := setupTestDB(t)

	// No rows → 0.
	v, err := d.ExpectedFloor("fp-floor")
	if err != nil {
		t.Fatalf("ExpectedFloor (empty): %v", err)
	}
	if v != 0 {
		t.Errorf("ExpectedFloor empty = %v, want 0", v)
	}

	// Insert an anomaly and mark it 'expected'.
	a := makeAnomaly("fp-floor")
	a.Observed = 50.0
	_, err = d.InsertOpenAnomaly(a)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	open, err := d.GetOpenAnomalyByFingerprint("fp-floor")
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}
	_, err = d.AckAnomaly(open.ID, "mark_expected", "alice", "ok", time.Now())
	if err != nil {
		t.Fatalf("AckAnomaly mark_expected: %v", err)
	}

	// Now insert a second row for the same fingerprint (first is no longer open).
	a2 := makeAnomaly("fp-floor")
	a2.Observed = 75.0
	_, err = d.InsertOpenAnomaly(a2)
	if err != nil {
		t.Fatalf("insert second: %v", err)
	}
	open2, err := d.GetOpenAnomalyByFingerprint("fp-floor")
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint second: %v", err)
	}
	_, err = d.AckAnomaly(open2.ID, "mark_expected", "alice", "ok again", time.Now())
	if err != nil {
		t.Fatalf("AckAnomaly second mark_expected: %v", err)
	}

	// ExpectedFloor must return MAX(50, 75) = 75.
	v, err = d.ExpectedFloor("fp-floor")
	if err != nil {
		t.Fatalf("ExpectedFloor: %v", err)
	}
	if v != 75.0 {
		t.Errorf("ExpectedFloor = %v, want 75.0", v)
	}

	// Different fingerprint → 0 (no expected rows for it).
	v2, err := d.ExpectedFloor("fp-floor-other")
	if err != nil {
		t.Fatalf("ExpectedFloor other: %v", err)
	}
	if v2 != 0 {
		t.Errorf("ExpectedFloor other = %v, want 0", v2)
	}
}

// TestAnomalyRepoMarkAnomalyNotified verifies notified_at is stamped correctly.
func TestAnomalyRepoMarkAnomalyNotified(t *testing.T) {
	d := setupTestDB(t)

	a := makeAnomaly("fp-notify")
	_, err := d.InsertOpenAnomaly(a)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	open, err := d.GetOpenAnomalyByFingerprint("fp-notify")
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}

	if open.NotifiedAt != nil {
		t.Errorf("notified_at before mark = %v, want nil", open.NotifiedAt)
	}

	notifiedAt := time.Now().UTC().Truncate(time.Second)
	err = d.MarkAnomalyNotified(open.ID, notifiedAt)
	if err != nil {
		t.Fatalf("MarkAnomalyNotified: %v", err)
	}

	got, err := d.GetAnomaly(open.ID)
	if err != nil {
		t.Fatalf("GetAnomaly: %v", err)
	}
	if got.NotifiedAt == nil {
		t.Fatal("notified_at still nil after MarkAnomalyNotified")
	}
	if !got.NotifiedAt.Equal(notifiedAt) {
		t.Errorf("notified_at = %v, want %v", got.NotifiedAt, notifiedAt)
	}
}

// TestAnomalyRepoUpsertJobBaseline verifies insert-then-update semantics
// and GetJobBaseline round-trip. Also verifies GetJobBaseline returns
// ErrNotFound for a missing job ID.
func TestAnomalyRepoUpsertJobBaseline(t *testing.T) {
	d := setupTestDB(t)

	// Need a real job to satisfy the FK constraint.
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "test-dest", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}
	jobID, err := d.CreateJob(Job{
		Name: "baseline-job", StorageDestID: destID,
		BackupTypeChain: "full", ContainerMode: "one_by_one",
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// GetJobBaseline for missing job → ErrNotFound.
	_, err = d.GetJobBaseline(99999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetJobBaseline(missing): want ErrNotFound, got %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	b := JobBaseline{
		JobID:          jobID,
		SampleCount:    10,
		BytesMedian:    1024.0,
		BytesMAD:       100.0,
		DurationMedian: 30.0,
		DurationMAD:    5.0,
		FailureRate:    0.1,
		UpdatedAt:      now,
	}
	if err := d.UpsertJobBaseline(b); err != nil {
		t.Fatalf("UpsertJobBaseline (insert): %v", err)
	}

	got, err := d.GetJobBaseline(jobID)
	if err != nil {
		t.Fatalf("GetJobBaseline: %v", err)
	}
	if got.SampleCount != 10 {
		t.Errorf("SampleCount = %d, want 10", got.SampleCount)
	}
	if got.BytesMedian != 1024.0 {
		t.Errorf("BytesMedian = %v, want 1024.0", got.BytesMedian)
	}
	if got.FailureRate != 0.1 {
		t.Errorf("FailureRate = %v, want 0.1", got.FailureRate)
	}

	// Update via upsert — change sample_count and bytes_median.
	b2 := b
	b2.SampleCount = 20
	b2.BytesMedian = 2048.0
	b2.UpdatedAt = now.Add(time.Minute)
	if err := d.UpsertJobBaseline(b2); err != nil {
		t.Fatalf("UpsertJobBaseline (update): %v", err)
	}

	got2, err := d.GetJobBaseline(jobID)
	if err != nil {
		t.Fatalf("GetJobBaseline after update: %v", err)
	}
	if got2.SampleCount != 20 {
		t.Errorf("after update SampleCount = %d, want 20", got2.SampleCount)
	}
	if got2.BytesMedian != 2048.0 {
		t.Errorf("after update BytesMedian = %v, want 2048.0", got2.BytesMedian)
	}
	// SampleCount must not be doubled — only one row exists.
	rows, err := d.Query("SELECT COUNT(*) FROM job_baselines WHERE job_id=?", jobID)
	if err != nil {
		t.Fatalf("count query: %v", err)
	}
	defer rows.Close()
	rows.Next()
	var count int
	if err := rows.Scan(&count); err != nil {
		t.Fatalf("scan count: %v", err)
	}
	if count != 1 {
		t.Errorf("job_baselines row count = %d, want 1 (upsert must not duplicate)", count)
	}
}

// TestAnomalyRepoInsertAndListCapacitySamples verifies ordering (ASC) and
// the Since filter on ListCapacitySamples.
func TestAnomalyRepoInsertAndListCapacitySamples(t *testing.T) {
	d := setupTestDB(t)

	destID, err := d.CreateStorageDestination(StorageDestination{Name: "cap-dest", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	samples := []CapacitySample{
		{DestID: destID, SampledAt: base.Add(0 * time.Hour), FreeBytes: 100, TotalBytes: 1000},
		{DestID: destID, SampledAt: base.Add(1 * time.Hour), FreeBytes: 90, TotalBytes: 1000},
		{DestID: destID, SampledAt: base.Add(2 * time.Hour), FreeBytes: 80, TotalBytes: 1000},
		{DestID: destID, SampledAt: base.Add(3 * time.Hour), FreeBytes: 70, TotalBytes: 1000},
	}
	for i, s := range samples {
		if err := d.InsertCapacitySample(s); err != nil {
			t.Fatalf("InsertCapacitySample %d: %v", i, err)
		}
	}

	// All samples (since = base).
	all, err := d.ListCapacitySamples(destID, base)
	if err != nil {
		t.Fatalf("ListCapacitySamples: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("got %d samples, want 4", len(all))
	}
	// Ordering must be ASC by sampled_at.
	for i := 1; i < len(all); i++ {
		if all[i].SampledAt.Before(all[i-1].SampledAt) {
			t.Errorf("samples not ASC at index %d: %v < %v", i, all[i].SampledAt, all[i-1].SampledAt)
		}
	}

	// Since filter: only samples at or after base+2h.
	since := base.Add(2 * time.Hour)
	filtered, err := d.ListCapacitySamples(destID, since)
	if err != nil {
		t.Fatalf("ListCapacitySamples Since: %v", err)
	}
	if len(filtered) != 2 {
		t.Errorf("Since=base+2h: got %d samples, want 2", len(filtered))
	}

	// Different dest → empty.
	empty, err := d.ListCapacitySamples(99999, base)
	if err != nil {
		t.Fatalf("ListCapacitySamples other dest: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("other dest: got %d samples, want 0", len(empty))
	}
}

// TestAnomalyRepoPruneOldAnomalies verifies:
//   - terminal rows older than cutoff are deleted, count returned.
//   - open rows are never deleted (even if older than cutoff).
//   - recent terminal rows (last_seen_at >= cutoff) are kept.
func TestAnomalyRepoPruneOldAnomalies(t *testing.T) {
	d := setupTestDB(t)

	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	old := cutoff.Add(-24 * time.Hour)

	// insertAt inserts an open anomaly seen at seenAt and returns its ID.
	// runID, when non-nil, is stamped on the row so it can later be resolved
	// via ResolveOpenAnomaliesForRun.
	insertAt := func(fp string, seenAt time.Time, runID *int64) int64 {
		a := makeAnomaly(fp)
		a.LastSeenAt = seenAt
		a.FirstSeenAt = seenAt
		a.JobRunID = runID
		_, err := d.InsertOpenAnomaly(a)
		if err != nil {
			t.Fatalf("insert %s: %v", fp, err)
		}
		open, err := d.GetOpenAnomalyByFingerprint(fp)
		if err != nil {
			t.Fatalf("GetOpenAnomalyByFingerprint %s: %v", fp, err)
		}
		return open.ID
	}

	// Old open row — must survive pruning.
	oldOpenID := insertAt("fp-prune-old-open", old, nil)

	// Old acknowledged row (state='acknowledged') — must be deleted.
	oldAckID := insertAt("fp-prune-old-ack", old, nil)
	if _, err := d.AckAnomaly(oldAckID, "dismiss", "sys", "old", old); err != nil {
		t.Fatalf("AckAnomaly old: %v", err)
	}

	// Old resolved row (state='resolved') — must be deleted. Resolve it
	// genuinely via ResolveOpenAnomaliesForRun so the state='resolved'
	// branch of PruneOldAnomalies' WHERE-IN is exercised.
	runID := int64(7001)
	oldResolvedID := insertAt("fp-prune-old-resolved", old, &runID)
	n, err := d.ResolveOpenAnomaliesForRun(runID, []string{"warning"}, old)
	if err != nil {
		t.Fatalf("ResolveOpenAnomaliesForRun: %v", err)
	}
	if n != 1 {
		t.Fatalf("ResolveOpenAnomaliesForRun resolved %d rows, want 1", n)
	}

	// Old expected row (state='expected') — must be deleted. mark_expected
	// transitions to state='expected', exercising that branch.
	oldExpectedID := insertAt("fp-prune-old-expected", old, nil)
	if _, err := d.AckAnomaly(oldExpectedID, "mark_expected", "sys", "old expected", old); err != nil {
		t.Fatalf("AckAnomaly mark_expected old: %v", err)
	}

	// Recent terminal row (at cutoff) — must survive.
	recentAckID := insertAt("fp-prune-recent-ack", cutoff, nil)
	if _, err := d.AckAnomaly(recentAckID, "dismiss", "sys", "recent", cutoff); err != nil {
		t.Fatalf("AckAnomaly recent: %v", err)
	}

	// Prune.
	deleted, err := d.PruneOldAnomalies(cutoff)
	if err != nil {
		t.Fatalf("PruneOldAnomalies: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted = %d, want 3 (old acknowledged + resolved + expected rows)", deleted)
	}

	// Old open row still present.
	if _, err := d.GetAnomaly(oldOpenID); err != nil {
		t.Errorf("old open row should survive prune: %v", err)
	}

	// All three old terminal rows gone.
	if _, err := d.GetAnomaly(oldAckID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old acknowledged row should be gone after prune: %v", err)
	}
	if _, err := d.GetAnomaly(oldResolvedID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old resolved row should be gone after prune: %v", err)
	}
	if _, err := d.GetAnomaly(oldExpectedID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old expected row should be gone after prune: %v", err)
	}

	// Recent ack row still present.
	if _, err := d.GetAnomaly(recentAckID); err != nil {
		t.Errorf("recent ack row should survive prune: %v", err)
	}
}

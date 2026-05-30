package anomaly

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// ── TestLifecyclePersistInsert ───────────────────────────────────────────────

// TestLifecyclePersistInsertAndRefresh verifies:
//   - First persist → exactly one open row, anomaly.raised broadcast.
//   - Second persist (same fingerprint, same severity, advanced clock) →
//     still exactly one open row, anomaly.updated broadcast, last_seen_at advanced.
func TestLifecyclePersistInsertAndRefresh(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	hub := &recordingBroadcaster{}
	ev := newEvaluatorWithBroadcaster(d, hub, &Registry{}, clk)

	_, runID := seedJobAndRun(t, d)
	a := makeTestAnomaly(1, runID, "size_bytes")

	// First persist.
	ev.persist(a)

	row, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint after first persist: %v", err)
	}
	if row.State != "open" {
		t.Errorf("state = %q, want 'open'", row.State)
	}
	firstLastSeen := row.LastSeenAt

	// Advance clock and persist again with the same fingerprint.
	clk.Advance(5 * time.Minute)
	ev.persist(a)

	row2, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint after second persist: %v", err)
	}

	// Still exactly one open row.
	if row2.ID != row.ID {
		t.Errorf("second persist created a new row (id %d → %d), expected same id", row.ID, row2.ID)
	}
	if !row2.LastSeenAt.After(firstLastSeen) {
		t.Errorf("last_seen_at not advanced: first=%v second=%v", firstLastSeen, row2.LastSeenAt)
	}

	// Hub must have exactly one anomaly.raised and one anomaly.updated.
	eventTypes := extractEventTypes(t, hub)
	assertEventCount(t, eventTypes, "anomaly.raised", 1)
	assertEventCount(t, eventTypes, "anomaly.updated", 1)
}

// ── TestLifecyclePersistEscalation ───────────────────────────────────────────

// TestLifecyclePersistEscalation: open as warning, then persist as critical →
// severity in DB becomes critical, anomaly.updated broadcast is emitted.
func TestLifecyclePersistEscalation(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	hub := &recordingBroadcaster{}
	ev := newEvaluatorWithBroadcaster(d, hub, &Registry{}, clk)

	_, runID := seedJobAndRun(t, d)
	a := makeTestAnomaly(1, runID, "duration_seconds")
	a.Severity = SeverityWarning

	// First persist: warning.
	ev.persist(a)
	row, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		t.Fatalf("first persist: %v", err)
	}
	if row.Severity != "warning" {
		t.Errorf("after first persist severity = %q, want 'warning'", row.Severity)
	}

	// Advance clock and persist again with critical severity.
	clk.Advance(10 * time.Minute)
	a.Severity = SeverityCritical
	ev.persist(a)

	row2, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		t.Fatalf("second persist: %v", err)
	}
	if row2.Severity != "critical" {
		t.Errorf("after escalation severity = %q, want 'critical'", row2.Severity)
	}

	eventTypes := extractEventTypes(t, hub)
	assertEventCount(t, eventTypes, "anomaly.raised", 1)
	assertEventCount(t, eventTypes, "anomaly.updated", 1)
}

// ── TestLifecyclePersistDowngrade ────────────────────────────────────────────

// TestLifecyclePersistDowngrade: open an anomaly as critical, then persist the
// same fingerprint with a lower severity (warning). The DB row must remain
// critical — severity is never downgraded. last_seen_at must advance and an
// anomaly.updated event must be broadcast.
func TestLifecyclePersistDowngrade(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	hub := &recordingBroadcaster{}
	ev := newEvaluatorWithBroadcaster(d, hub, &Registry{}, clk)

	_, runID := seedJobAndRun(t, d)
	a := makeTestAnomaly(1, runID, "downgrade_metric")
	a.Severity = SeverityCritical

	// First persist: critical.
	ev.persist(a)
	row, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint after first persist: %v", err)
	}
	if row.Severity != "critical" {
		t.Errorf("after first persist severity = %q, want 'critical'", row.Severity)
	}
	firstLastSeen := row.LastSeenAt

	// Advance clock and persist same fingerprint with a lower severity.
	clk.Advance(10 * time.Minute)
	a.Severity = SeverityWarning
	ev.persist(a)

	row2, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint after second persist: %v", err)
	}

	// Severity must NOT have been downgraded.
	if row2.Severity != "critical" {
		t.Errorf("severity after downgrade attempt = %q, want 'critical' (must not downgrade)", row2.Severity)
	}

	// last_seen_at must have advanced.
	if !row2.LastSeenAt.After(firstLastSeen) {
		t.Errorf("last_seen_at not advanced: first=%v second=%v", firstLastSeen, row2.LastSeenAt)
	}

	// anomaly.updated must have been broadcast.
	eventTypes := extractEventTypes(t, hub)
	assertEventCount(t, eventTypes, "anomaly.raised", 1)
	assertEventCount(t, eventTypes, "anomaly.updated", 1)
}

// ── TestLifecycleResolveSoftAnomalies ────────────────────────────────────────

// TestLifecycleResolveSoftAnomalies: open info + warning + critical for a run.
// After resolveSoftAnomalies(runID) info+warning must be resolved; critical open.
func TestLifecycleResolveSoftAnomalies(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))
	hub := &recordingBroadcaster{}
	ev := newEvaluatorWithBroadcaster(d, hub, &Registry{}, clk)

	jobID, runID := seedJobAndRun(t, d)

	// Insert three open anomalies for the same run.
	infoFP := Fingerprint("det", ScopeJob, jobID, "metric_info")
	warnFP := Fingerprint("det", ScopeJob, jobID, "metric_warn")
	critFP := Fingerprint("det", ScopeJob, jobID, "metric_crit")

	insertOpen := func(fp, severity string) {
		t.Helper()
		now := clk.Now()
		_, err := d.InsertOpenAnomaly(db.Anomaly{
			Fingerprint: fp, Detector: "det", Severity: severity,
			ScopeKind: "job", ScopeID: jobID, Metric: fp,
			Observed: 1.0, Summary: "test", Details: "",
			State: "open", FirstSeenAt: now, LastSeenAt: now,
			JobRunID: &runID,
		})
		if err != nil {
			t.Fatalf("InsertOpenAnomaly %q: %v", fp, err)
		}
	}
	insertOpen(infoFP, "info")
	insertOpen(warnFP, "warning")
	insertOpen(critFP, "critical")

	ev.resolveSoftAnomalies(runID)

	// info should be resolved.
	infoRow, err := d.GetOpenAnomalyByFingerprint(infoFP)
	if err == nil {
		t.Errorf("info anomaly still open (id=%d), expected resolved", infoRow.ID)
	}

	// warning should be resolved.
	warnRow, err := d.GetOpenAnomalyByFingerprint(warnFP)
	if err == nil {
		t.Errorf("warning anomaly still open (id=%d), expected resolved", warnRow.ID)
	}

	// critical should still be open.
	critRow, err := d.GetOpenAnomalyByFingerprint(critFP)
	if err != nil {
		t.Errorf("critical anomaly not open anymore: %v", err)
	} else if critRow.State != "open" {
		t.Errorf("critical anomaly state = %q, want 'open'", critRow.State)
	}

	// Exactly one anomaly.bulk_resolved summary event must have been broadcast,
	// carrying the right run_id and resolved count (2 soft rows).
	assertEventCount(t, extractEventTypes(t, hub), "anomaly.bulk_resolved", 1)
	if rid, n, ok := findBulkResolved(t, hub); !ok {
		t.Error("no anomaly.bulk_resolved event found")
	} else {
		if rid != runID {
			t.Errorf("bulk_resolved run_id = %d, want %d", rid, runID)
		}
		if n != 2 {
			t.Errorf("bulk_resolved count = %d, want 2", n)
		}
	}
}

// ── TestLifecycleAck ─────────────────────────────────────────────────────────

// TestLifecycleAck: ack an open row → true, state transitions, anomaly.updated.
// Ack on already-terminal row → false, no extra broadcast.
func TestLifecycleAck(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC))
	hub := &recordingBroadcaster{}
	ev := newEvaluatorWithBroadcaster(d, hub, &Registry{}, clk)

	_, runID := seedJobAndRun(t, d)
	a := makeTestAnomaly(1, runID, "failure_rate")
	ev.persist(a)

	row, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}

	// Ack the open row.
	acked, err := ev.Ack(row.ID, AckDismiss, "user@example.com", "expected noise")
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if !acked {
		t.Error("Ack returned false, want true")
	}

	// Verify state is now 'acknowledged'.
	updated, err := d.GetAnomaly(row.ID)
	if err != nil {
		t.Fatalf("GetAnomaly after ack: %v", err)
	}
	if updated.State != "acknowledged" {
		t.Errorf("state after ack = %q, want 'acknowledged'", updated.State)
	}

	// anomaly.updated should have been broadcast.
	eventTypes := extractEventTypes(t, hub)
	assertEventCount(t, eventTypes, "anomaly.updated", 1)

	prevBroadcastCount := len(hub.messages())

	// Ack the already-acknowledged row → must return false, no new broadcast.
	acked2, err := ev.Ack(row.ID, AckDismiss, "user@example.com", "duplicate")
	if err != nil {
		t.Fatalf("second Ack: %v", err)
	}
	if acked2 {
		t.Error("second Ack returned true, want false (already terminal)")
	}
	if len(hub.messages()) != prevBroadcastCount {
		t.Errorf("hub received extra message after second Ack on terminal row")
	}

	// Now exercise the mark_expected action through the public Ack method on a
	// fresh open anomaly → state must become 'expected', an anomaly.updated
	// broadcast must fire, and an activity row must be written.
	b := makeTestAnomaly(1, runID, "size_bytes")
	ev.persist(b)
	bRow, err := d.GetOpenAnomalyByFingerprint(b.Fingerprint)
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint (mark_expected): %v", err)
	}

	logsBefore, _ := d.ListActivityLogs(100, "anomaly")
	updatedBefore := countEvent(extractEventTypes(t, hub), "anomaly.updated")

	expAcked, err := ev.Ack(bRow.ID, AckMarkExpected, "user@example.com", "this is normal")
	if err != nil {
		t.Fatalf("Ack(mark_expected): %v", err)
	}
	if !expAcked {
		t.Error("Ack(mark_expected) returned false, want true")
	}

	expRow, err := d.GetAnomaly(bRow.ID)
	if err != nil {
		t.Fatalf("GetAnomaly after mark_expected: %v", err)
	}
	if expRow.State != "expected" {
		t.Errorf("state after mark_expected = %q, want 'expected'", expRow.State)
	}

	if got := countEvent(extractEventTypes(t, hub), "anomaly.updated"); got != updatedBefore+1 {
		t.Errorf("anomaly.updated count after mark_expected = %d, want %d", got, updatedBefore+1)
	}

	logsAfter, _ := d.ListActivityLogs(100, "anomaly")
	if len(logsAfter) <= len(logsBefore) {
		t.Errorf("expected a new anomaly activity row after mark_expected; before=%d after=%d",
			len(logsBefore), len(logsAfter))
	}
}

// ── TestLifecycleBulkAck ─────────────────────────────────────────────────────

// TestLifecycleBulkAck: mix of open and already-terminal IDs → correct counts.
func TestLifecycleBulkAck(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC))
	hub := &recordingBroadcaster{}
	ev := newEvaluatorWithBroadcaster(d, hub, &Registry{}, clk)

	jobID, runID := seedJobAndRun(t, d)

	// Insert 3 open anomalies.
	var ids []int64
	for i, metric := range []string{"m1", "m2", "m3"} {
		fp := Fingerprint("det", ScopeJob, jobID, metric)
		now := clk.Now()
		rid := runID
		_, err := d.InsertOpenAnomaly(db.Anomaly{
			Fingerprint: fp, Detector: "det", Severity: "warning",
			ScopeKind: "job", ScopeID: jobID, Metric: metric,
			Observed: float64(i + 1), Summary: "test", State: "open",
			FirstSeenAt: now, LastSeenAt: now, JobRunID: &rid,
		})
		if err != nil {
			t.Fatalf("InsertOpenAnomaly %q: %v", metric, err)
		}
		row, _ := d.GetOpenAnomalyByFingerprint(fp)
		ids = append(ids, row.ID)
	}

	// Pre-ack the first one to make it terminal.
	_, _ = d.AckAnomaly(ids[0], "dismiss", "pre", "", clk.Now())

	// BulkAck all three.
	acked, skipped, err := ev.BulkAck(ids, AckDismiss, "admin", "bulk test")
	if err != nil {
		t.Fatalf("BulkAck: %v", err)
	}
	if acked != 2 {
		t.Errorf("acknowledged = %d, want 2", acked)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}

	// Exactly one anomaly.bulk_acked summary event must have been broadcast.
	assertEventCount(t, extractEventTypes(t, hub), "anomaly.bulk_acked", 1)

	// A single summary activity_log row must have been written.
	logs, err := d.ListActivityLogs(100, "anomaly")
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}
	bulkRows := 0
	for _, l := range logs {
		if strings.Contains(l.Message, "acknowledged via bulk action") {
			bulkRows++
		}
	}
	if bulkRows != 1 {
		t.Errorf("bulk-action activity rows = %d, want 1", bulkRows)
	}
}

// ── TestLifecycleExpectedFloor ───────────────────────────────────────────────

// TestLifecycleExpectedFloor: verify EvalContext.ExpectedFloor and ApplyFloor.
func TestLifecycleExpectedFloor(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC))
	ev := newEvaluatorWithBroadcaster(d, &recordingBroadcaster{}, &Registry{}, clk)

	ec, err := buildContextForFloorTest(t, d, ev)
	if err != nil {
		t.Fatalf("buildContextForFloorTest: %v", err)
	}

	fp := "test-fingerprint-floor"

	// No expected rows yet → floor is 0, ApplyFloor returns base unchanged.
	if got := ec.ExpectedFloor(fp); got != 0 {
		t.Errorf("ExpectedFloor (no rows) = %v, want 0", got)
	}
	base := 100.0
	if got := ec.ApplyFloor(fp, base, 3.0, 5.0); got != base {
		t.Errorf("ApplyFloor (floor=0) = %v, want %v", got, base)
	}

	// Seed an expected-state row with observed=200.
	now := clk.Now()
	rid := int64(0)
	_, err = d.InsertOpenAnomaly(db.Anomaly{
		Fingerprint: fp, Detector: "det", Severity: "warning",
		ScopeKind: "job", ScopeID: 1, Metric: "m",
		Observed: 200.0, Summary: "test", State: "open",
		FirstSeenAt: now, LastSeenAt: now, JobRunID: &rid,
	})
	if err != nil {
		t.Fatalf("InsertOpenAnomaly: %v", err)
	}
	row, _ := d.GetOpenAnomalyByFingerprint(fp)
	_, _ = d.AckAnomaly(row.ID, "mark_expected", "user", "", clk.Now())

	// Rebuild floorLookup to pick up the new row.
	ec.floorLookup = func(f string) float64 {
		v, _ := d.ExpectedFloor(f)
		return v
	}

	// floor = 200, k=3, mad=5 → floor+k*mad = 215. base=100 → ApplyFloor = 215.
	if got := ec.ExpectedFloor(fp); got != 200 {
		t.Errorf("ExpectedFloor (with row) = %v, want 200", got)
	}
	want := 200.0 + 3.0*5.0 // 215
	if got := ec.ApplyFloor(fp, base, 3.0, 5.0); got != want {
		t.Errorf("ApplyFloor = %v, want %v", got, want)
	}

	// base > floor+k*mad: ApplyFloor returns base.
	highBase := 300.0
	if got := ec.ApplyFloor(fp, highBase, 3.0, 5.0); got != highBase {
		t.Errorf("ApplyFloor (base > floor) = %v, want %v", got, highBase)
	}

	// Nil lookup: ExpectedFloor returns 0.
	ec.floorLookup = nil
	if got := ec.ExpectedFloor(fp); got != 0 {
		t.Errorf("ExpectedFloor (nil lookup) = %v, want 0", got)
	}
}

// ── TestLifecycleActivityLog ─────────────────────────────────────────────────

// TestLifecycleActivityLog: after a raise and an ack, at least two anomaly-
// category activity rows must exist.
func TestLifecycleActivityLog(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC))
	ev := newEvaluatorWithBroadcaster(d, &recordingBroadcaster{}, &Registry{}, clk)

	_, runID := seedJobAndRun(t, d)
	a := makeTestAnomaly(1, runID, "activity_metric")

	// Raise.
	ev.persist(a)
	row, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}

	// Ack.
	_, _ = ev.Ack(row.ID, AckDismiss, "tester", "test reason")

	// Query activity_log rows with category='anomaly'.
	logs, err := d.ListActivityLogs(50, "anomaly")
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}
	if len(logs) < 2 {
		t.Errorf("expected at least 2 anomaly activity log entries, got %d", len(logs))
		for i, l := range logs {
			t.Logf("  [%d] level=%s message=%s", i, l.Level, l.Message)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// extractEventTypes returns the "type" string from all WebSocket messages the
// hub has received.
func extractEventTypes(t *testing.T, hub *recordingBroadcaster) []string {
	t.Helper()
	var types []string
	for _, m := range hub.messages() {
		var env map[string]json.RawMessage
		if err := json.Unmarshal(m, &env); err != nil {
			continue
		}
		var typ string
		if err := json.Unmarshal(env["type"], &typ); err != nil {
			continue
		}
		types = append(types, typ)
	}
	return types
}

// assertEventCount checks that eventType appears exactly want times in types.
func assertEventCount(t *testing.T, types []string, eventType string, want int) {
	t.Helper()
	if got := countEvent(types, eventType); got != want {
		t.Errorf("event %q count = %d, want %d; all events: %v", eventType, got, want, types)
	}
}

// countEvent returns how many times eventType appears in types.
func countEvent(types []string, eventType string) int {
	got := 0
	for _, et := range types {
		if et == eventType {
			got++
		}
	}
	return got
}

// findBulkResolved locates the first "anomaly.bulk_resolved" event and decodes
// its run_id and count from the data payload. ok is false when no such event
// was broadcast.
func findBulkResolved(t *testing.T, hub *recordingBroadcaster) (runID int64, count int64, ok bool) {
	t.Helper()
	for _, m := range hub.messages() {
		var env struct {
			Type string `json:"type"`
			Data struct {
				RunID int64 `json:"run_id"`
				Count int64 `json:"count"`
			} `json:"data"`
		}
		if err := json.Unmarshal(m, &env); err != nil {
			continue
		}
		if env.Type == "anomaly.bulk_resolved" {
			return env.Data.RunID, env.Data.Count, true
		}
	}
	return 0, 0, false
}

// buildContextForFloorTest builds an EvalContext pointing at the given db so
// that floorLookup is wired to the real ExpectedFloor query. It seeds a minimal
// job+run to satisfy buildContext's DB reads. Returns the EvalContext and any
// error.
func buildContextForFloorTest(t *testing.T, d *db.DB, ev *Evaluator) (EvalContext, error) {
	t.Helper()
	_, runID := seedJobAndRun(t, d)
	run, err := d.GetJobRun(runID)
	if err != nil {
		return EvalContext{}, err
	}
	job, err := d.GetJob(run.JobID)
	if err != nil {
		return EvalContext{}, err
	}
	return EvalContext{
		JobRun:            &run,
		Job:               &job,
		GlobalSensitivity: "balanced",
		Clock:             ev.clock,
		floorLookup: func(fp string) float64 {
			v, _ := d.ExpectedFloor(fp)
			return v
		},
	}, nil
}

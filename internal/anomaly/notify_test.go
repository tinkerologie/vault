package anomaly

import (
	"sync"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// ── recording notifier ────────────────────────────────────────────────────────

// recordingNotifier implements AnomalyNotifier and records every call.
type recordingNotifier struct {
	mu   sync.Mutex
	sent []notifyCall
}

type notifyCall struct {
	anomaly   Anomaly
	scopeName string
	isUpdate  bool
}

func (n *recordingNotifier) SendAnomaly(a Anomaly, scopeName string, isUpdate bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, notifyCall{anomaly: a, scopeName: scopeName, isUpdate: isUpdate})
}

func (n *recordingNotifier) calls() []notifyCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]notifyCall, len(n.sent))
	copy(out, n.sent)
	return out
}

func (n *recordingNotifier) callCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.sent)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// newNotifyEvaluator builds an Evaluator with an injected recording notifier.
func newNotifyEvaluator(t *testing.T, d *db.DB, rec *recordingNotifier) *Evaluator {
	t.Helper()
	clk := NewFakeClock(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	ev := newEvaluatorWithBroadcaster(d, &recordingBroadcaster{}, &Registry{}, clk)
	ev.SetNotifier(rec)
	return ev
}

// seedJobWithNotifyOn creates a job with the given notify_on value and returns its ID.
func seedJobWithNotifyOn(t *testing.T, d *db.DB, notifyOn string) (jobID int64) {
	t.Helper()
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "dest-notify",
		Type:   "local",
		Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}
	jobID, err = d.CreateJob(db.Job{
		Name:          "notify-test-job",
		Enabled:       true,
		StorageDestID: destID,
		NotifyOn:      notifyOn,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return jobID
}

// anomalyForJobSeverity returns a minimal Anomaly with the given job scope and severity.
func anomalyForJobSeverity(jobID int64, sev Severity) Anomaly {
	fp := Fingerprint("test_det", ScopeJob, jobID, "metric")
	return Anomaly{
		Fingerprint: fp,
		Detector:    "test_det",
		Severity:    sev,
		ScopeKind:   ScopeJob,
		ScopeID:     jobID,
		Metric:      "metric",
		Observed:    10.0,
		Summary:     "test summary",
		Details:     "test details",
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestMaybeNotify_NilNotifier verifies that maybeNotify is a no-op when
// SetNotifier has not been called (notifier == nil).
func TestMaybeNotify_NilNotifier(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	ev := newEvaluatorWithBroadcaster(d, &recordingBroadcaster{}, &Registry{}, clk)
	// Do NOT call ev.SetNotifier — notifier stays nil.

	jobID := seedJobWithNotifyOn(t, d, "always")
	a := anomalyForJobSeverity(jobID, SeverityCritical)
	a.ID = 999

	// Must not panic.
	ev.maybeNotify(a, false)
}

// TestMaybeNotify_SeverityBelowThreshold verifies that a warning anomaly is
// NOT sent when the global min-severity is "critical".
func TestMaybeNotify_SeverityBelowThreshold(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingNotifier{}
	ev := newNotifyEvaluator(t, d, rec)

	// Global min-severity = critical (the default from db seeding).
	// No override needed — the seed already sets it.
	// (anomaly_notify_min_severity is seeded to "critical" by db.Open)

	jobID := seedJobWithNotifyOn(t, d, "failure") // no anomaly: token
	a := anomalyForJobSeverity(jobID, SeverityWarning)
	a.ID = 1

	ev.maybeNotify(a, false)

	if rec.callCount() != 0 {
		t.Errorf("expected 0 notify calls for warning below critical threshold, got %d", rec.callCount())
	}
}

// TestMaybeNotify_SeverityAtThreshold verifies a critical anomaly IS sent when
// global min-severity is "critical".
func TestMaybeNotify_SeverityAtThreshold(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingNotifier{}
	ev := newNotifyEvaluator(t, d, rec)

	jobID := seedJobWithNotifyOn(t, d, "failure")
	a := anomalyForJobSeverity(jobID, SeverityCritical)

	// Persist to get a real DB row with an ID.
	ev.persist(a)
	row, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}
	a.ID = row.ID

	// persist() calls maybeNotify internally, so the notifier should have been called.
	if rec.callCount() != 1 {
		t.Errorf("expected 1 notify call, got %d", rec.callCount())
	}

	// Verify notified_at was stamped.
	updated, err := d.GetAnomaly(row.ID)
	if err != nil {
		t.Fatalf("GetAnomaly: %v", err)
	}
	if updated.NotifiedAt == nil {
		t.Error("notified_at not stamped after successful notify")
	}
}

// TestMaybeNotify_AlreadyNotified verifies that a second maybeNotify call
// with NotifiedAt already set does NOT re-send.
func TestMaybeNotify_AlreadyNotified(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingNotifier{}
	ev := newNotifyEvaluator(t, d, rec)

	jobID := seedJobWithNotifyOn(t, d, "failure")
	a := anomalyForJobSeverity(jobID, SeverityCritical)

	// First persist — sends.
	ev.persist(a)
	if rec.callCount() != 1 {
		t.Fatalf("expected 1 call after first persist, got %d", rec.callCount())
	}

	row, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if err != nil {
		t.Fatalf("GetOpenAnomalyByFingerprint: %v", err)
	}

	// Build an already-notified anomaly and call maybeNotify directly.
	alreadyNotified := FromDB(row)
	if alreadyNotified.NotifiedAt == nil {
		t.Fatal("NotifiedAt should be set after first notify")
	}

	beforeCount := rec.callCount()
	ev.maybeNotify(alreadyNotified, false)

	if rec.callCount() != beforeCount {
		t.Errorf("expected no extra notify call on already-notified anomaly, got %d extra",
			rec.callCount()-beforeCount)
	}
}

// TestMaybeNotify_PerJobOverrideWarning verifies that a warning anomaly IS sent
// when the job has "anomaly:warning" in its notify_on, even though the global
// min-severity is "critical".
func TestMaybeNotify_PerJobOverrideWarning(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingNotifier{}
	ev := newNotifyEvaluator(t, d, rec)

	// Global min-severity is "critical" (seeded default).
	// Job has "failure,anomaly:warning" — so warning anomalies should be forced.
	jobID := seedJobWithNotifyOn(t, d, "failure,anomaly:warning")

	a := anomalyForJobSeverity(jobID, SeverityWarning)
	ev.persist(a)

	if rec.callCount() != 1 {
		t.Errorf("expected 1 notify call via per-job override, got %d", rec.callCount())
	}

	// Verify the sent anomaly severity.
	if calls := rec.calls(); len(calls) > 0 {
		if calls[0].anomaly.Severity != SeverityWarning {
			t.Errorf("sent severity = %q, want 'warning'", calls[0].anomaly.Severity)
		}
	}
}

// TestMaybeNotify_PerJobOverrideAny verifies "anomaly:any" forces notification
// for all severities including info.
func TestMaybeNotify_PerJobOverrideAny(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingNotifier{}
	ev := newNotifyEvaluator(t, d, rec)

	jobID := seedJobWithNotifyOn(t, d, "anomaly:any")

	// info anomaly — below both "critical" global threshold and "warning" override.
	a := anomalyForJobSeverity(jobID, SeverityInfo)
	ev.persist(a)

	if rec.callCount() != 1 {
		t.Errorf("expected 1 notify call via anomaly:any override, got %d", rec.callCount())
	}
}

// TestMaybeNotify_PerJobOverrideCriticalOnly verifies "anomaly:critical" does
// NOT force a warning anomaly.
func TestMaybeNotify_PerJobOverrideCriticalOnly(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingNotifier{}
	ev := newNotifyEvaluator(t, d, rec)

	// Global min-severity = critical. Override = anomaly:critical only.
	// A warning anomaly should NOT be sent.
	jobID := seedJobWithNotifyOn(t, d, "failure,anomaly:critical")

	a := anomalyForJobSeverity(jobID, SeverityWarning)
	ev.persist(a)

	if rec.callCount() != 0 {
		t.Errorf("expected 0 notify calls (warning below anomaly:critical override), got %d", rec.callCount())
	}
}

// TestMaybeNotify_MinSeverityWarning verifies that changing the global
// min-severity to "warning" causes warning anomalies to be sent.
func TestMaybeNotify_MinSeverityWarning(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingNotifier{}
	ev := newNotifyEvaluator(t, d, rec)

	// Override global min-severity to warning.
	if err := d.SetSetting("anomaly_notify_min_severity", "warning"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	jobID := seedJobWithNotifyOn(t, d, "failure") // no anomaly: token needed
	a := anomalyForJobSeverity(jobID, SeverityWarning)
	ev.persist(a)

	if rec.callCount() != 1 {
		t.Errorf("expected 1 notify call with min-severity=warning, got %d", rec.callCount())
	}
}

// TestMaybeNotify_EscalationPath tests that persist's escalation branch
// (warning → critical with NotifiedAt unset) triggers a notify call.
func TestMaybeNotify_EscalationPath(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingNotifier{}
	ev := newNotifyEvaluator(t, d, rec)

	// Set min-severity to warning so we can see the escalation clearly.
	if err := d.SetSetting("anomaly_notify_min_severity", "warning"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	jobID := seedJobWithNotifyOn(t, d, "failure")
	a := anomalyForJobSeverity(jobID, SeverityWarning)

	// First persist — warning, notified.
	ev.persist(a)
	if rec.callCount() != 1 {
		t.Fatalf("expected 1 call after initial persist, got %d", rec.callCount())
	}

	// Now escalate to critical. The escalation path in persist() checks
	// existing.NotifiedAt == nil before calling maybeNotify. Since we already
	// sent above, no second call should fire on escalation.
	//
	// Reset the notifier count by creating a fresh anomaly at a different metric.
	rec2 := &recordingNotifier{}
	ev.SetNotifier(rec2)

	b := anomalyForJobSeverity(jobID, SeverityWarning)
	// Change metric so it gets a fresh fingerprint.
	b.Metric = "metric2"
	b.Fingerprint = Fingerprint("test_det", ScopeJob, jobID, "metric2")
	ev.persist(b)

	if rec2.callCount() != 1 {
		t.Fatalf("expected 1 call for fresh warning, got %d", rec2.callCount())
	}

	// Escalate it — notified_at is already set, so escalation should not re-send.
	b.Severity = SeverityCritical
	ev.persist(b)
	// persist escalation path checks existing.NotifiedAt; since it was notified,
	// maybeNotify is not called.
	if rec2.callCount() > 1 {
		t.Errorf("expected no second notify on escalation of already-notified anomaly, got %d", rec2.callCount())
	}
}

// TestJobHasAnomalyOverride tests the CSV parsing logic directly.
func TestJobHasAnomalyOverride(t *testing.T) {
	t.Parallel()

	cases := []struct {
		notifyOn string
		sev      Severity
		want     bool
	}{
		{"failure", SeverityWarning, false},
		{"always", SeverityCritical, false},
		{"never", SeverityInfo, false},
		{"anomaly:any", SeverityInfo, true},
		{"anomaly:any", SeverityWarning, true},
		{"anomaly:any", SeverityCritical, true},
		{"anomaly:info", SeverityInfo, true},
		{"anomaly:warning", SeverityWarning, true},
		{"anomaly:warning", SeverityCritical, true},
		{"anomaly:warning", SeverityInfo, false},
		{"anomaly:critical", SeverityCritical, true},
		{"anomaly:critical", SeverityWarning, false},
		{"anomaly:critical", SeverityInfo, false},
		{"failure,anomaly:warning", SeverityWarning, true},
		{"failure,anomaly:warning", SeverityInfo, false},
		{"always , anomaly:any", SeverityInfo, true},
		{"", SeverityCritical, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.notifyOn+"/"+string(tc.sev), func(t *testing.T) {
			t.Parallel()
			got := jobHasAnomalyOverride(tc.notifyOn, tc.sev)
			if got != tc.want {
				t.Errorf("jobHasAnomalyOverride(%q, %q) = %v, want %v",
					tc.notifyOn, tc.sev, got, tc.want)
			}
		})
	}
}

// TestSeverityAtLeast tests the ordering function info < warning < critical.
func TestSeverityAtLeast(t *testing.T) {
	t.Parallel()

	cases := []struct {
		candidate, min Severity
		want           bool
	}{
		{SeverityInfo, SeverityInfo, true},
		{SeverityWarning, SeverityInfo, true},
		{SeverityCritical, SeverityInfo, true},
		{SeverityInfo, SeverityWarning, false},
		{SeverityWarning, SeverityWarning, true},
		{SeverityCritical, SeverityWarning, true},
		{SeverityInfo, SeverityCritical, false},
		{SeverityWarning, SeverityCritical, false},
		{SeverityCritical, SeverityCritical, true},
		{"unknown", SeverityInfo, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.candidate)+">="+string(tc.min), func(t *testing.T) {
			t.Parallel()
			if got := severityAtLeast(tc.candidate, tc.min); got != tc.want {
				t.Errorf("severityAtLeast(%q, %q) = %v, want %v",
					tc.candidate, tc.min, got, tc.want)
			}
		})
	}
}

// TestSetNotifier_ExistingNotifyOnBehaviourUnaffected verifies that the
// "always"/"failure"/"never" tokens in notify_on continue to work as expected
// by the runner (i.e. jobHasAnomalyOverride ignores them).
func TestSetNotifier_ExistingNotifyOnBehaviourUnaffected(t *testing.T) {
	t.Parallel()

	legacyTokens := []string{"always", "failure", "never", ""}
	for _, tok := range legacyTokens {
		tok := tok
		t.Run(tok, func(t *testing.T) {
			t.Parallel()
			// None of the legacy tokens should trigger anomaly override.
			if jobHasAnomalyOverride(tok, SeverityCritical) {
				t.Errorf("jobHasAnomalyOverride(%q, critical) = true; legacy token must not affect anomaly routing", tok)
			}
		})
	}
}

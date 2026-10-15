package anomaly

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/ws"
)

// execSQL runs a raw statement against the test DB (used to inject faults via
// triggers or by renaming a table out from under the repository layer).
func execSQL(t *testing.T, d *db.DB, stmt string) {
	t.Helper()
	if _, err := d.Exec(stmt); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
}

func countEvents(t *testing.T, hub *recordingBroadcaster, typ string) int {
	t.Helper()
	n := 0
	for _, et := range extractEventTypes(t, hub) {
		if et == typ {
			n++
		}
	}
	return n
}

func newLifecycle458(t *testing.T) (*db.DB, *Evaluator, *recordingBroadcaster, *FakeClock) {
	t.Helper()
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC))
	hub := &recordingBroadcaster{}
	return d, newEvaluatorWithBroadcaster(d, hub, &Registry{}, clk), hub, clk
}

func TestLifecycle458_PersistDBFailures(t *testing.T) {
	d, ev, hub, _ := newLifecycle458(t)
	jobID, runID := seedJobAndRun(t, d)
	a := makeTestAnomaly(jobID, runID, "size_bytes")

	execSQL(t, d, `CREATE TRIGGER f_ins BEFORE INSERT ON anomalies BEGIN SELECT RAISE(ABORT, 'no insert'); END`)
	ev.persist(a)
	if n := len(hub.messages()); n != 0 {
		t.Fatalf("insert failure broadcast %d messages", n)
	}
	execSQL(t, d, `DROP TRIGGER f_ins`)

	ev.persist(a) // raised
	execSQL(t, d, `CREATE TRIGGER f_upd BEFORE UPDATE ON anomalies BEGIN SELECT RAISE(ABORT, 'no update'); END`)
	ev.persist(a) // refresh fails
	if got := countEvents(t, hub, "anomaly.updated"); got != 0 {
		t.Errorf("refresh failure emitted %d updates", got)
	}
}

func TestLifecycle458_ResolveSoftAnomalies(t *testing.T) {
	d, ev, hub, _ := newLifecycle458(t)
	jobID, runID := seedJobAndRun(t, d)
	ev.persist(makeTestAnomaly(jobID, runID, "size_bytes"))
	ev.resolveSoftAnomalies(runID)
	if got := countEvents(t, hub, "anomaly.bulk_resolved"); got != 1 {
		t.Fatalf("bulk_resolved events = %d, want 1", got)
	}
	execSQL(t, d, `ALTER TABLE anomalies RENAME TO anomalies_hidden`)
	ev.resolveSoftAnomalies(runID) // logged, no panic
}

func TestLifecycle458_AckAndBulkAck(t *testing.T) {
	d, ev, hub, _ := newLifecycle458(t)
	jobID, runID := seedJobAndRun(t, d)
	ev.persist(makeTestAnomaly(jobID, runID, "m1"))
	ev.persist(makeTestAnomaly(jobID, runID, "m2"))
	r1, err := d.GetOpenAnomalyByFingerprint(Fingerprint("stub_detector", ScopeJob, jobID, "m1"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := d.GetOpenAnomalyByFingerprint(Fingerprint("stub_detector", ScopeJob, jobID, "m2"))
	if err != nil {
		t.Fatal(err)
	}

	ok, err := ev.Ack(r1.ID, AckAction("dismiss"), "tester", "known")
	if err != nil || !ok {
		t.Fatalf("Ack = %v, %v", ok, err)
	}
	ok, err = ev.Ack(r1.ID, AckAction("dismiss"), "tester", "again")
	if err != nil || ok {
		t.Fatalf("second Ack = %v, %v, want false,nil", ok, err)
	}

	acked, skipped, err := ev.BulkAck([]int64{r1.ID, r2.ID}, AckAction("dismiss"), "tester", "bulk")
	if err != nil || acked != 1 || skipped != 1 {
		t.Fatalf("BulkAck = %d/%d, %v", acked, skipped, err)
	}
	if got := countEvents(t, hub, "anomaly.bulk_acked"); got != 1 {
		t.Errorf("bulk_acked events = %d", got)
	}

	execSQL(t, d, `ALTER TABLE anomalies RENAME TO anomalies_hidden`)
	if _, err := ev.Ack(r2.ID, AckAction("dismiss"), "t", ""); err == nil {
		t.Error("Ack on broken DB: want error")
	}
	if _, _, err := ev.BulkAck([]int64{r2.ID}, AckAction("dismiss"), "t", ""); err == nil {
		t.Error("BulkAck on broken DB: want error")
	}
}

func TestLifecycle458_PruneOldAnomalies(t *testing.T) {
	d, ev, _, clk := newLifecycle458(t)
	jobID, runID := seedJobAndRun(t, d)
	ev.persist(makeTestAnomaly(jobID, runID, "old"))
	row, err := d.GetOpenAnomalyByFingerprint(Fingerprint("stub_detector", ScopeJob, jobID, "old"))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ev.Ack(row.ID, AckAction("dismiss"), "t", ""); err != nil || !ok {
		t.Fatalf("Ack = %v, %v", ok, err)
	}
	clk.Advance(time.Duration(anomalyRetentionDays+10) * 24 * time.Hour)
	ev.PruneOldAnomalies()
	if _, err := d.GetAnomaly(row.ID); err == nil {
		t.Error("old acknowledged anomaly not pruned")
	}
	execSQL(t, d, `ALTER TABLE anomalies RENAME TO anomalies_hidden`)
	ev.PruneOldAnomalies() // logged, no panic
}

func TestLifecycle458_EvaluateTrendDetectors(t *testing.T) {
	d, ev, hub, clk := newLifecycle458(t)

	// No trend detectors registered: returns before touching samples.
	ev.EvaluateTrendDetectors()

	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "trend-dest", Type: "local", Config: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InsertCapacitySample(db.CapacitySample{DestID: destID, SampledAt: clk.Now().Add(-time.Hour), FreeBytes: 10, TotalBytes: 100}); err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint("trend_stub", ScopeDestination, destID, "free")
	ev.reg.Register(stubDetector{name: "trend_stub", kind: KindTrend, out: []Anomaly{{
		Fingerprint: fp, Detector: "trend_stub", Severity: SeverityWarning,
		ScopeKind: ScopeDestination, ScopeID: destID, Metric: "free", Summary: "low",
	}}})
	ev.EvaluateTrendDetectors()
	if got := countEvents(t, hub, "anomaly.raised"); got != 1 {
		t.Fatalf("raised = %d, want 1", got)
	}

	execSQL(t, d, `ALTER TABLE destination_capacity_samples RENAME TO dcs_hidden`)
	ev.EvaluateTrendDetectors() // per-destination error is logged and skipped
	execSQL(t, d, `ALTER TABLE storage_destinations RENAME TO sd_hidden`)
	ev.EvaluateTrendDetectors() // listing error aborts the pass
}

func TestLifecycle458_BuildContextAndBaselineFailures(t *testing.T) {
	d, ev, hub, _ := newLifecycle458(t)
	_, runID := seedJobAndRun(t, d)

	execSQL(t, d, `ALTER TABLE job_baselines RENAME TO jb_hidden`)
	if _, err := ev.buildContext(runID); err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Errorf("baseline lookup failure: %v", err)
	}

	secs := 60
	runs := make([]db.JobRun, minBaselineSamples)
	for i := range runs {
		runs[i] = db.JobRun{DurationSeconds: &secs, SizeBytes: 100, Status: "completed"}
	}
	ev.refreshBaseline(EvalContext{Job: &db.Job{ID: 1}, RecentRuns: runs})
	if got := countEvents(t, hub, "baseline.updated"); got != 0 {
		t.Errorf("baseline.updated emitted despite upsert failure")
	}
	execSQL(t, d, `ALTER TABLE jb_hidden RENAME TO job_baselines`)

	execSQL(t, d, `ALTER TABLE storage_destinations RENAME TO sd_hidden`)
	if _, err := ev.buildContext(runID); err == nil || !strings.Contains(err.Error(), "storage destination") {
		t.Errorf("destination lookup failure: %v", err)
	}
}

func TestLifecycle458_BroadcastMarshalFailureIsDropped(t *testing.T) {
	_, ev, hub, _ := newLifecycle458(t)
	ev.broadcastData("x", map[string]any{"bad": make(chan int)})
	if n := len(hub.messages()); n != 0 {
		t.Errorf("unmarshalable payload broadcast %d messages", n)
	}
	ev.hub = nil
	ev.broadcastData("y", 1) // nil hub is a no-op
}

func TestNotify458_ScopeNamesAndGates(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingNotifier{}
	ev := newNotifyEvaluator(t, d, rec)
	jobID := seedJobWithNotifyOn(t, d, "")
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "named-dest", Type: "local", Config: `{}`})
	if err != nil {
		t.Fatal(err)
	}

	if got := resolveAnomalyScopeName(ev, Anomaly{ScopeKind: ScopeDestination, ScopeID: destID}); got != "named-dest" {
		t.Errorf("destination scope name = %q", got)
	}
	if got := resolveAnomalyScopeName(ev, Anomaly{ScopeKind: ScopeDestination, ScopeID: 9999}); got != "" {
		t.Errorf("missing destination name = %q", got)
	}
	if got := resolveAnomalyScopeName(ev, Anomaly{ScopeKind: ScopeJob, ScopeID: 9999}); got != "" {
		t.Errorf("missing job name = %q", got)
	}
	if got := resolveAnomalyScopeName(ev, Anomaly{ScopeKind: ScopeKind("other")}); got != "" {
		t.Errorf("unknown scope name = %q", got)
	}

	// A critical anomaly with no DB id is never sent (it could not be deduped).
	a := anomalyForJobSeverity(jobID, SeverityCritical)
	ev.maybeNotify(a, false)
	if rec.callCount() != 0 {
		t.Fatal("id=0 anomaly was sent")
	}

	// Unreadable settings fall back to the default (critical) gate.
	execSQL(t, d, `ALTER TABLE settings RENAME TO settings_hidden`)
	a.ID = 77
	ev.maybeNotify(a, false)
	if rec.callCount() != 1 {
		t.Fatalf("calls = %d, want 1", rec.callCount())
	}
}

func TestNotify458_JobOverrideTokens(t *testing.T) {
	cases := []struct {
		notifyOn string
		sev      Severity
		want     bool
	}{
		{"anomaly:warning", SeverityCritical, true},
		{"anomaly:warning", SeverityInfo, false},
		{"anomaly:critical", SeverityWarning, false},
		{"always, anomaly:info", SeverityInfo, true},
	}
	for _, tc := range cases {
		if got := jobHasAnomalyOverride(tc.notifyOn, tc.sev); got != tc.want {
			t.Errorf("jobHasAnomalyOverride(%q,%s) = %v", tc.notifyOn, tc.sev, got)
		}
	}
	if severityRank(Severity("bogus")) != -1 {
		t.Error("unknown severity should rank below info")
	}
}

func TestNotify458_RealNotifierDispatch(t *testing.T) {
	a := Anomaly{Severity: SeverityCritical, ScopeKind: ScopeJob, Summary: "s", Details: `{"window_size":3}`}
	(&realNotifier{}).SendAnomaly(a, "job", false) // nil URL func: Unraid only
	NewRealNotifier(func() string { return "" }).SendAnomaly(a, "job", true)
	// An invalid (non-https) webhook is rejected inside the async sender
	// without any network I/O.
	NewRealNotifier(func() string { return "ftp://invalid.example" }).SendAnomaly(a, "job", false)
}

func TestReliability458_VerifyQueryFailureIsNonFatal(t *testing.T) {
	d := openTestDB(t)
	execSQL(t, d, `ALTER TABLE verify_runs RENAME TO vr_hidden`)
	det := NewReliabilityDetector(d)
	out, err := det.Evaluate(EvalContext{Job: &db.Job{ID: 1, Name: "j"}})
	if err != nil || len(out) != 0 {
		t.Fatalf("Evaluate = %v, %v", out, err)
	}
	if out, err := det.Evaluate(EvalContext{}); out != nil || err != nil {
		t.Errorf("nil job = %v, %v", out, err)
	}
}

func TestDetectors458_IdentityAndNilGuards(t *testing.T) {
	d := openTestDB(t)
	dets := []Detector{
		NewCapacityTrajectoryDetector(d),
		NewDurationDriftDetector(),
		NewReliabilityDetector(d),
		NewSizeDriftDetector(),
	}
	wantKinds := []Kind{KindTrend, KindPerRun, KindPerRun, KindPerRun}
	for i, det := range dets {
		if det.Name() == "" || det.Kind() != wantKinds[i] {
			t.Errorf("%T: name=%q kind=%v", det, det.Name(), det.Kind())
		}
	}
	for _, det := range []Detector{NewDurationDriftDetector(), NewSizeDriftDetector()} {
		if out, err := det.Evaluate(EvalContext{}); out != nil || err != nil {
			t.Errorf("%T with empty context = %v, %v", det, out, err)
		}
	}
	// No recent runs: no failure streak.
	if a := NewReliabilityDetector(d).evalStreak(EvalContext{}, 1); a != nil {
		t.Errorf("empty streak = %+v", a)
	}
}

func TestEvaluator458_ConstructorAndSettingsFallback(t *testing.T) {
	d := openTestDB(t)
	ev := NewEvaluator(d, ws.NewHub(), &Registry{}, RealClock{})
	if ev.hub == nil || cap(ev.ch) != queueCap {
		t.Fatalf("NewEvaluator wiring: cap=%d", cap(ev.ch))
	}
	execSQL(t, d, `ALTER TABLE settings RENAME TO settings_hidden`)
	if got := ev.globalSensitivity(); got == "" {
		t.Error("globalSensitivity should fall back to the documented default")
	}
}

func TestHumanize458_Extremes(t *testing.T) {
	if got := commaGroup(-999); got != "-999" {
		t.Errorf("commaGroup(-999) = %q", got)
	}
	if got := clampInt64(math.Inf(-1)); got != math.MinInt64 {
		t.Errorf("clampInt64(-Inf) = %d", got)
	}
}

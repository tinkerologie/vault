package anomaly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// newEvaluatorWithBroadcaster is the test-only constructor that injects a stub
// broadcaster in place of a real *ws.Hub.
func newEvaluatorWithBroadcaster(d *db.DB, b broadcaster, r *Registry, c Clock) *Evaluator {
	return &Evaluator{
		db:    d,
		hub:   b,
		reg:   r,
		clock: c,
		ch:    make(chan int64, queueCap),
		done:  make(chan struct{}),
	}
}

// openTestDB opens a fresh SQLite database in a temp directory.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedJobAndRun inserts a minimal Job and one completed JobRun, returning both IDs.
func seedJobAndRun(t *testing.T, d *db.DB) (jobID, runID int64) {
	t.Helper()

	// Create a storage destination the job can reference.
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "test-dest",
		Type:   "local",
		Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}

	jobID, err = d.CreateJob(db.Job{
		Name:          "test-job",
		Enabled:       true,
		StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	runID, err = d.CreateJobRun(db.JobRun{
		JobID:      jobID,
		Status:     "running",
		BackupType: "full",
		RunType:    "backup",
	})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}

	// Mark the run completed so DurationSeconds is set.
	if err := d.UpdateJobRun(db.JobRun{
		ID:          runID,
		Status:      "success",
		ItemsDone:   1,
		ItemsFailed: 0,
		SizeBytes:   1024,
	}); err != nil {
		t.Fatalf("UpdateJobRun: %v", err)
	}

	return jobID, runID
}

// recordingBroadcaster captures all Broadcast calls for test assertions.
type recordingBroadcaster struct {
	mu   sync.Mutex
	msgs [][]byte
}

func (r *recordingBroadcaster) Broadcast(msg []byte) {
	cp := make([]byte, len(msg))
	copy(cp, msg)
	r.mu.Lock()
	r.msgs = append(r.msgs, cp)
	r.mu.Unlock()
}

func (r *recordingBroadcaster) messages() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.msgs))
	copy(out, r.msgs)
	return out
}

// evalStubDetector is a Detector that returns a fixed set of anomalies.
// Named differently from the detector_test.go stubDetector to avoid redeclaration.
type evalStubDetector struct {
	name      string
	anomalies []Anomaly
	err       error
}

func (s *evalStubDetector) Name() string                              { return s.name }
func (s *evalStubDetector) Kind() Kind                                { return KindPerRun }
func (s *evalStubDetector) Evaluate(_ EvalContext) ([]Anomaly, error) { return s.anomalies, s.err }

// evalPanicDetector panics when Evaluate is called.
type evalPanicDetector struct{ name string }

func (p *evalPanicDetector) Name() string { return p.name }
func (p *evalPanicDetector) Kind() Kind   { return KindPerRun }
func (p *evalPanicDetector) Evaluate(_ EvalContext) ([]Anomaly, error) {
	panic("intentional panic in " + p.name)
}

// makeTestAnomaly returns a minimal Anomaly with a valid fingerprint for tests.
func makeTestAnomaly(jobID int64, runID int64, metric string) Anomaly {
	fp := Fingerprint("stub_detector", ScopeJob, jobID, metric)
	rid := runID
	return Anomaly{
		Fingerprint: fp,
		Detector:    "stub_detector",
		Severity:    SeverityWarning,
		ScopeKind:   ScopeJob,
		ScopeID:     jobID,
		Metric:      metric,
		Observed:    42.0,
		JobRunID:    &rid,
		Summary:     "test anomaly",
		Details:     "test details",
	}
}

// captureLog redirects the standard logger to a *bytes.Buffer for the duration
// of the test, restoring the original output on cleanup.
func captureLog(t *testing.T) *safeBuffer {
	t.Helper()
	buf := &safeBuffer{}
	old := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

// safeBuffer wraps bytes.Buffer with a mutex for concurrent log writes.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestEvaluatorHappyPath: register a stub detector returning one anomaly,
// seed a job+run, Start, EnqueueRun, poll until the row is persisted, then
// assert the hub received an anomaly.raised message.
func TestEvaluatorHappyPath(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	hub := &recordingBroadcaster{}
	reg := &Registry{}

	jobID, runID := seedJobAndRun(t, d)

	a := makeTestAnomaly(jobID, runID, "size_bytes")
	reg.Register(&evalStubDetector{name: "stub_detector", anomalies: []Anomaly{a}})

	ev := newEvaluatorWithBroadcaster(d, hub, reg, clk)
	ev.Start()
	ev.EnqueueRun(runID)

	// Poll (up to 2 s) until the anomaly row appears in the DB.
	var persistedID int64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		row, err := d.GetOpenAnomalyByFingerprint(a.Fingerprint)
		if err == nil {
			persistedID = row.ID
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if persistedID == 0 {
		t.Fatal("anomaly was not persisted within 2 s")
	}

	// Drain and verify the row is still there.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ev.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	// Verify the anomaly was fetched by ID.
	_, err := d.GetAnomaly(persistedID)
	if err != nil {
		t.Fatalf("GetAnomaly(%d): %v", persistedID, err)
	}

	// Verify the hub received at least one anomaly.raised message.
	msgs := hub.messages()
	found := false
	for _, m := range msgs {
		var env map[string]json.RawMessage
		if err := json.Unmarshal(m, &env); err != nil {
			continue
		}
		var typ string
		if err := json.Unmarshal(env["type"], &typ); err != nil {
			continue
		}
		if typ == "anomaly.raised" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hub did not receive anomaly.raised; messages: %v",
			func() []string {
				s := make([]string, len(msgs))
				for i, m := range msgs {
					s[i] = string(m)
				}
				return s
			}())
	}
}

// TestEvaluatorQueueOverflow: enqueue 200 run IDs into a cap-64 channel
// without starting the worker; verify the WARN log fires and the caller never
// blocks (completing the loop is the proof).
func TestEvaluatorQueueOverflow(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Now())
	hub := &recordingBroadcaster{}
	reg := &Registry{}

	logBuf := captureLog(t)

	ev := newEvaluatorWithBroadcaster(d, hub, reg, clk)
	// Do NOT call ev.Start() — we want the channel to fill up.

	// Enqueue 200 IDs. The function must return promptly (non-blocking).
	for i := int64(1); i <= 200; i++ {
		ev.EnqueueRun(i)
	}

	// Drain without starting — just close the done channel.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ev.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	// At least one "queue full" WARN must have been logged.
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "WARN anomaly: queue full") {
		t.Errorf("expected WARN anomaly: queue full in log; got:\n%s", logOutput)
	}
}

// TestEvaluatorPanicIsolation: register a panicking detector AND a normal
// stub detector. After processing, assert the normal detector's anomaly was
// persisted (panic in one didn't prevent the other) and a recovery was logged.
func TestEvaluatorPanicIsolation(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	hub := &recordingBroadcaster{}
	reg := &Registry{}

	jobID, runID := seedJobAndRun(t, d)

	// Register the panicking detector first.
	reg.Register(&evalPanicDetector{name: "panicky"})

	// Register the normal detector second.
	goodAnomaly := makeTestAnomaly(jobID, runID, "duration_seconds")
	goodAnomaly.Fingerprint = Fingerprint("good_detector", ScopeJob, jobID, "duration_seconds")
	goodAnomaly.Detector = "good_detector"
	reg.Register(&evalStubDetector{name: "good_detector", anomalies: []Anomaly{goodAnomaly}})

	logBuf := captureLog(t)

	ev := newEvaluatorWithBroadcaster(d, hub, reg, clk)
	ev.Start()
	ev.EnqueueRun(runID)

	// Poll until the good anomaly appears.
	deadline := time.Now().Add(2 * time.Second)
	var persistedID int64
	for time.Now().Before(deadline) {
		row, err := d.GetOpenAnomalyByFingerprint(goodAnomaly.Fingerprint)
		if err == nil {
			persistedID = row.ID
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ev.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if persistedID == 0 {
		t.Fatal("good_detector anomaly was not persisted — panic in panicky detector may have leaked")
	}

	// Confirm the panic was recovered and logged.
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, `detector "panicky" panicked`) {
		t.Errorf("expected panic recovery log; got:\n%s", logOutput)
	}
}

// TestEvaluatorDrainIdempotent: calling Drain twice must not panic.
func TestEvaluatorDrainIdempotent(t *testing.T) {
	d := openTestDB(t)
	ev := newEvaluatorWithBroadcaster(d, &recordingBroadcaster{}, &Registry{}, NewFakeClock(time.Now()))
	ev.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := ev.Drain(ctx); err != nil {
		t.Fatalf("first Drain: %v", err)
	}
	// Second Drain must not panic; it should return quickly.
	if err := ev.Drain(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("second Drain unexpected error: %v", err)
	}
}

// TestEvaluatorBuildContextMissingRun: EnqueueRun with a non-existent run ID
// must log an error and not panic.
func TestEvaluatorBuildContextMissingRun(t *testing.T) {
	d := openTestDB(t)
	reg := &Registry{}

	stub := &evalStubDetector{name: "stub", anomalies: nil}
	reg.Register(stub)

	logBuf := captureLog(t)

	ev := newEvaluatorWithBroadcaster(d, &recordingBroadcaster{}, reg, NewFakeClock(time.Now()))
	ev.Start()
	ev.EnqueueRun(99999) // non-existent

	// Poll for the side effect (the logged buildContext error). We poll rather
	// than relying on Drain alone because the worker's select on {done, ch} is
	// non-deterministic: once done is closed it may win the race and exit
	// before the queued run is processed. Polling guarantees the run is handled.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), "buildContext") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ev.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if !strings.Contains(logBuf.String(), "buildContext") {
		t.Errorf("expected buildContext error in log; got:\n%s", logBuf.String())
	}
}

// TestEvaluatorNoDetectors: with no detectors registered the evaluator must
// process the run without error and compute a baseline (if enough samples).
func TestEvaluatorNoDetectors(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	reg := &Registry{} // empty

	_, runID := seedJobAndRun(t, d)

	ev := newEvaluatorWithBroadcaster(d, &recordingBroadcaster{}, reg, clk)
	ev.Start()
	ev.EnqueueRun(runID)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Drain is the synchronization barrier — it returns only after the worker
	// goroutine has exited. This test only asserts no panic / no deadlock, so
	// no further wait is needed.
	if err := ev.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// TestEvaluatorRefreshBaseline: seed enough completed runs to cross the
// minBaselineSamples threshold and verify UpsertJobBaseline is called.
func TestEvaluatorRefreshBaseline(t *testing.T) {
	d := openTestDB(t)
	clk := NewFakeClock(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	reg := &Registry{}

	// Create destination and job.
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{}`})
	jobID, _ := d.CreateJob(db.Job{Name: "baseline-job", Enabled: true, StorageDestID: destID})

	// Seed minBaselineSamples completed runs.
	var lastRunID int64
	for i := 0; i < minBaselineSamples; i++ {
		rid, _ := d.CreateJobRun(db.JobRun{
			JobID: jobID, Status: "running", BackupType: "full", RunType: "backup",
		})
		_ = d.UpdateJobRun(db.JobRun{
			ID: rid, Status: "success", ItemsDone: 1, SizeBytes: int64(100 + i*10),
		})
		lastRunID = rid
	}

	ev := newEvaluatorWithBroadcaster(d, &recordingBroadcaster{}, reg, clk)
	ev.Start()
	ev.EnqueueRun(lastRunID)

	// Poll until baseline row appears.
	deadline := time.Now().Add(2 * time.Second)
	var baselineFound bool
	for time.Now().Before(deadline) {
		_, err := d.GetJobBaseline(jobID)
		if err == nil {
			baselineFound = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ev.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if !baselineFound {
		t.Fatal("baseline was not upserted after enough completed runs")
	}
}

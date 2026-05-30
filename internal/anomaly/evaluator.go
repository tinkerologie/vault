package anomaly

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"runtime/debug"
	"sync"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/ws"
)

const queueCap = 64

// minBaselineSamples is the minimum number of completed runs needed before
// we compute (or refresh) a baseline. Fewer samples produce unreliable stats.
const minBaselineSamples = 3

// recentRunsWindow is how many of a job's most recent runs buildContext loads
// for the EvalContext (newest first).
const recentRunsWindow = 10

// broadcaster is the subset of *ws.Hub used by the Evaluator. Defined as an
// interface so tests can inject a recording stub without spinning up a real hub.
type broadcaster interface {
	Broadcast(msg []byte)
}

// Evaluator is the async worker that runs per-run anomaly detectors after
// each backup run completes. It operates on a bounded channel to decouple
// the caller (runner) from the (potentially slow) detection work.
//
// Concurrency contract:
//   - A single goroutine is spawned by Start(); all db/detector calls happen
//     inside that goroutine so no additional locking is required for those.
//   - EnqueueRun may be called from any goroutine concurrently; it only
//     touches the buffered ch channel which is safe by design.
//   - Start is guarded by startOnce so the single-worker invariant holds even
//     if Start is called more than once.
//   - Drain closes the done channel exactly once via drainOnce.
type Evaluator struct {
	db    *db.DB
	hub   broadcaster
	reg   *Registry
	clock Clock

	ch   chan int64
	done chan struct{}
	wg   sync.WaitGroup

	startOnce sync.Once
	drainOnce sync.Once
}

// NewEvaluator constructs an Evaluator backed by a real *ws.Hub.
// Call Start() to begin processing.
func NewEvaluator(d *db.DB, h *ws.Hub, r *Registry, c Clock) *Evaluator {
	return &Evaluator{
		db:    d,
		hub:   h,
		reg:   r,
		clock: c,
		ch:    make(chan int64, queueCap),
		done:  make(chan struct{}),
	}
}

// EnqueueRun enqueues runID for anomaly evaluation. It is non-blocking: if
// the channel is already full, the OLDEST pending id is dropped (with a WARN
// log) and the new id is placed at the tail.
func (e *Evaluator) EnqueueRun(runID int64) {
	select {
	case e.ch <- runID:
		// Fast path: channel has room.
	default:
		// Channel full — drain the oldest pending id to make room.
		select {
		case dropped := <-e.ch:
			log.Printf("WARN anomaly: queue full, dropped run %d", dropped)
		default:
			// A concurrent goroutine already drained; proceed.
		}
		// Non-blocking re-attempt: if another concurrent EnqueueRun filled the
		// slot we just opened, drop the new id rather than blocking.
		select {
		case e.ch <- runID:
		default:
			log.Printf("WARN anomaly: queue full, dropped run %d", runID)
		}
	}
}

// Start spawns the single worker goroutine. It is guarded by startOnce, so a
// second call is a no-op and the single-worker invariant is preserved.
func (e *Evaluator) Start() {
	e.startOnce.Do(func() {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			for {
				select {
				case <-e.done:
					return
				case runID := <-e.ch:
					e.evaluateRun(runID)
				}
			}
		}()
	})
}

// Drain signals the worker to stop and waits for it to exit, or until ctx is
// cancelled.
//
// drainOnce closes the done channel exactly once, so concurrent or repeated
// calls are safe. Each call spawns a short-lived waiter goroutine that blocks
// on wg.Wait(); once the worker has already exited (wg zero) this returns
// promptly. On ctx timeout, Drain returns ctx.Err() and the waiter goroutine
// is orphaned until the in-flight evaluateRun finishes and the worker exits;
// the caller cannot observe that later completion.
func (e *Evaluator) Drain(ctx context.Context) error {
	e.drainOnce.Do(func() { close(e.done) })

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// On timeout the wg.Wait() goroutine is orphaned until the in-flight
		// evaluateRun finishes; the caller cannot observe that completion.
		return ctx.Err()
	}
}

// evaluateRun is the main per-run pipeline: build context, run detectors,
// refresh baseline. All errors are logged; none propagate to the caller.
func (e *Evaluator) evaluateRun(runID int64) {
	ec, err := e.buildContext(runID)
	if err != nil {
		log.Printf("ERROR anomaly: buildContext(run %d): %v", runID, err)
		return
	}

	for _, det := range e.reg.PerRun() {
		e.runDetector(det, ec)
	}

	e.refreshBaseline(ec)
}

// runDetector calls a single detector, recovering from panics so that one
// broken detector cannot prevent the others from running.
func (e *Evaluator) runDetector(det Detector, ec EvalContext) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ERROR anomaly: detector %q panicked: %v\n%s",
				det.Name(), r, debug.Stack())
		}
	}()

	anomalies, err := det.Evaluate(ec)
	if err != nil {
		log.Printf("WARN anomaly: detector %q: %v", det.Name(), err)
		return
	}
	for _, a := range anomalies {
		e.persist(a)
	}
}

// persist saves a newly-detected anomaly.
//
// MINIMAL implementation for Task 5: sets State=open and timestamps, then
// calls InsertOpenAnomaly. On a fresh insert it broadcasts "anomaly.raised".
// If the row already exists (already-open conflict), this is a no-op here —
// Task 6 will add refresh + escalation logic.
func (e *Evaluator) persist(a Anomaly) {
	now := e.clock.Now()

	a.State = StateOpen
	if a.FirstSeenAt.IsZero() {
		a.FirstSeenAt = now
	}
	a.LastSeenAt = now

	inserted, err := e.db.InsertOpenAnomaly(ToDB(a))
	if err != nil {
		log.Printf("ERROR anomaly: persist %q: %v", a.Fingerprint, err)
		return
	}

	if !inserted {
		// Already-open conflict: no-op. Task 6 adds refresh + escalation.
		return
	}

	// Fetch the persisted row to get the auto-assigned primary key ID.
	row, fetchErr := e.db.GetOpenAnomalyByFingerprint(a.Fingerprint)
	if fetchErr != nil {
		// Non-fatal: broadcast with the data we have (ID will be 0).
		log.Printf("WARN anomaly: persist fetch after insert %q: %v", a.Fingerprint, fetchErr)
		e.broadcastAnomaly("anomaly.raised", a)
		return
	}
	e.broadcastAnomaly("anomaly.raised", FromDB(row))
}

// broadcastAnomaly marshals an anomaly event and sends it to the WebSocket hub.
// The real *ws.Hub.Broadcast sends on a buffered channel (cap 256); it only
// blocks when 256 messages are already queued, an extremely rare scenario.
// In tests, the stub broadcaster is always non-blocking.
func (e *Evaluator) broadcastAnomaly(eventType string, a Anomaly) {
	if e.hub == nil {
		return
	}

	payload := map[string]any{
		"type": eventType,
		"data": a,
	}
	msg, err := json.Marshal(payload)
	if err != nil {
		log.Printf("WARN anomaly: broadcast marshal %q: %v", eventType, err)
		return
	}

	e.hub.Broadcast(msg)
}

// buildContext loads all data needed for evaluation and assembles an EvalContext.
func (e *Evaluator) buildContext(runID int64) (EvalContext, error) {
	run, err := e.db.GetJobRun(runID)
	if err != nil {
		return EvalContext{}, err
	}

	job, err := e.db.GetJob(run.JobID)
	if err != nil {
		return EvalContext{}, err
	}

	// Destination: tolerate not-found (job may have had its dest deleted).
	dest, err := e.db.GetStorageDestination(job.StorageDestID)
	var destPtr *db.StorageDestination
	if err == nil {
		destPtr = &dest
	} else if !errors.Is(err, db.ErrNotFound) {
		return EvalContext{}, err
	}

	// Most recent runs for this job, newest first.
	recentRuns, err := e.db.GetJobRuns(run.JobID, recentRunsWindow)
	if err != nil {
		return EvalContext{}, err
	}

	// Baseline — tolerate not-found (no baseline yet for this job).
	baseline, err := e.db.GetJobBaseline(run.JobID)
	var baselinePtr *db.JobBaseline
	if err == nil {
		baselinePtr = &baseline
	} else if !errors.Is(err, db.ErrNotFound) {
		return EvalContext{}, err
	}

	// Global sensitivity setting ("balanced" is the seeded default).
	sensitivity, err := e.db.GetSetting("anomaly_sensitivity_default", "balanced")
	if err != nil {
		sensitivity = "balanced"
	}

	return EvalContext{
		JobRun:            &run,
		Job:               &job,
		Destination:       destPtr,
		RecentRuns:        recentRuns,
		Baseline:          baselinePtr,
		CapacitySamples:   nil, // per-run detectors don't need capacity samples
		GlobalSensitivity: sensitivity,
		Clock:             e.clock,
	}, nil
}

// refreshBaseline recomputes and upserts the job baseline from recent runs.
// Requires at least minBaselineSamples completed runs. Skips silently if there
// are not enough samples.
//
// Fields used from db.JobRun:
//   - SizeBytes       — bytes transferred (total archive size for this run)
//   - DurationSeconds — wall-clock seconds (NULL for in-progress runs, skipped)
//   - Status          — "success" = non-failure; anything else counts as failure
func (e *Evaluator) refreshBaseline(ec EvalContext) {
	if ec.Job == nil {
		return
	}

	// Filter to completed runs only (DurationSeconds != nil means completed).
	var completed []db.JobRun
	for _, r := range ec.RecentRuns {
		if r.DurationSeconds != nil {
			completed = append(completed, r)
		}
	}
	if len(completed) < minBaselineSamples {
		return
	}

	bytesVals := make([]float64, len(completed))
	durVals := make([]float64, len(completed))
	failCount := 0
	for i, r := range completed {
		bytesVals[i] = float64(r.SizeBytes)
		durVals[i] = float64(*r.DurationSeconds)
		if r.Status != "success" {
			failCount++
		}
	}

	baseline := db.JobBaseline{
		JobID:          ec.Job.ID,
		SampleCount:    len(completed),
		BytesMedian:    Median(bytesVals),
		BytesMAD:       MAD(bytesVals),
		DurationMedian: Median(durVals),
		DurationMAD:    MAD(durVals),
		FailureRate:    float64(failCount) / float64(len(completed)),
		UpdatedAt:      e.clock.Now(),
	}

	if err := e.db.UpsertJobBaseline(baseline); err != nil {
		log.Printf("WARN anomaly: upsert baseline for job %d: %v", ec.Job.ID, err)
	}
	// TODO(Task 15): broadcast a typed "baseline.updated" event.
}

// Ensure *ws.Hub satisfies the broadcaster interface at compile time.
var _ broadcaster = (*ws.Hub)(nil)

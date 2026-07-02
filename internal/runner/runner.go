// Package runner orchestrates backup job execution.
// It ties together the database, engine handlers, storage adapters,
// and WebSocket hub to actually run backup and restore operations.
package runner

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/notify"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/tempdir"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// Stall watchdog intervals, shared by the backup and restore run paths so the
// two cannot silently diverge. The watchdog warns after stallWarnInterval of
// no progress and cancels the run after stallCancelTimeout.
const (
	stallWarnInterval  = 30 * time.Minute
	stallCancelTimeout = 2 * time.Hour
)

// Runner executes backup and restore operations for jobs.
// RunStatus holds the state of the currently executing backup/restore.
// It is returned by Runner.Status() so the API can inform late-joining
// WebSocket clients or freshly loaded dashboards.
type RunStatus struct {
	Active             bool         `json:"active"`
	JobID              int64        `json:"job_id,omitempty"`
	RunID              int64        `json:"run_id,omitempty"`
	JobName            string       `json:"job_name,omitempty"`
	RunType            string       `json:"run_type,omitempty"`
	ItemsTotal         int          `json:"items_total,omitempty"`
	ItemsDone          int          `json:"items_done,omitempty"`
	ItemsFailed        int          `json:"items_failed,omitempty"`
	StartedAt          string       `json:"started_at,omitempty"`
	CurrentItem        string       `json:"current_item,omitempty"`
	CurrentItemType    string       `json:"current_item_type,omitempty"`
	CurrentItemPercent int          `json:"current_item_percent,omitempty"`
	CurrentItemMessage string       `json:"current_item_message,omitempty"`
	Cancelling         bool         `json:"cancelling,omitempty"`
	Queue              []QueueEntry `json:"queue,omitempty"`
}

// QueueEntry represents a job waiting to run.
type QueueEntry struct {
	JobID    int64  `json:"job_id"`
	JobName  string `json:"job_name"`
	QueuedAt string `json:"queued_at"`
}

// runOptions controls auxiliary behaviour for a single job invocation.
// The zero value represents a normal scheduled run. Manual and retry
// runs deviate from the default in distinct ways:
//   - manual runs do NOT have a retry scheduled on failure (the user
//     can re-press the Run-now button)
//   - retry runs carry retryOfRunID/retryAttempt forward so the
//     resulting job_run row links back to the original failure.
type runOptions struct {
	retryOfRunID int64
	retryAttempt int
	manual       bool
}

// retryOfRunIDPtr returns the nullable pointer form for db.JobRun. The
// model uses *int64 so JSON serialises NULL as `null` instead of a
// {Valid, Int64} struct.
func (o runOptions) retryOfRunIDPtr() *int64 {
	if o.retryOfRunID <= 0 {
		return nil
	}
	v := o.retryOfRunID
	return &v
}

type restoreProgressReporter struct {
	JobID       int64
	RunID       int64
	ItemName    string
	ItemType    string
	ItemsDone   int
	ItemsFailed int
	ItemsTotal  int
}

// anomalyEnqueuer is the subset of *anomaly.Evaluator used by Runner.
// Defined as an interface so:
//   - runner does not need to import the anomaly package (avoiding a potential
//     import cycle if anomaly ever needs something from runner in the future),
//   - tests can inject a lightweight fake without spinning up a real Evaluator.
//
// The real *anomaly.Evaluator satisfies this interface automatically.
type anomalyEnqueuer interface {
	EnqueueRun(runID int64)
}

// Runner executes backup and restore operations for jobs.
type Runner struct {
	db              *db.DB
	hub             *ws.Hub
	serverKey       []byte // AES-256 key for unsealing secrets.
	snapshotManager *db.SnapshotManager
	breaker         *Breaker
	mu              sync.Mutex

	// evaluator is an optional anomaly evaluator. When non-nil, EnqueueRun is
	// called (non-blocking) after every run completes so the anomaly engine can
	// inspect the run asynchronously. Nil means anomaly detection is disabled
	// for this Runner (e.g. in unit tests that don't need it).
	evaluator anomalyEnqueuer

	// statusMu protects the live run status fields below.
	statusMu   sync.RWMutex
	currentRun *RunStatus

	// cancelMu protects the active cancellation function.
	cancelMu        sync.Mutex
	cancelFn        context.CancelFunc
	cancellingJobID int64

	// lastProgress tracks the last progress update time for stall detection.
	lastProgressMu sync.Mutex
	lastProgress   time.Time

	// queueMu protects the pending job queue.
	queueMu sync.Mutex
	queue   []QueueEntry

	// Drain coordination. activeMu guards activeCount and draining.
	// activeCond is signalled when activeCount transitions to 0 so Drain
	// wakes without polling.
	activeMu    sync.Mutex
	activeCond  *sync.Cond
	activeCount int
	draining    bool
}

// Finalization step timeouts (issue #112). Each blocking call that runs after
// a backup completes is wrapped with runFinalizationStep so a single hung step
// cannot hold the run-slot mutex indefinitely. These constants name the
// per-step limits: generous enough for normal slow storage but bounded so the
// runner always eventually releases r.mu and lets the next job proceed.
const (
	finalizeManifestTimeout  = 5 * time.Minute
	finalizeDBBackupTimeout  = 5 * time.Minute
	finalizeSnapshotTimeout  = 5 * time.Minute
	finalizeRetentionTimeout = 10 * time.Minute
	finalizeNotifyTimeout    = 2 * time.Minute
)

// New creates a new Runner.
func New(database *db.DB, hub *ws.Hub, serverKey []byte) *Runner {
	r := &Runner{
		db:        database,
		hub:       hub,
		serverKey: serverKey,
	}
	failThreshold, _ := database.GetSettingInt("breaker_fail_threshold", 3)
	closeSuccesses, _ := database.GetSettingInt("breaker_close_successes", 2)
	r.breaker = NewBreaker(failThreshold, closeSuccesses)
	r.activeCond = sync.NewCond(&r.activeMu)
	return r
}

// Drain blocks until no job is running or ctx is done. After Drain is
// called, new RunJob invocations are refused with a logged warning.
// Intended for the api shutdown sequence: call with a bounded ctx
// (e.g. 30 s) so a planned restart finishes the active per-file upload
// cleanly.
func (r *Runner) Drain(ctx context.Context) error {
	r.activeMu.Lock()
	r.draining = true
	r.activeMu.Unlock()

	done := make(chan struct{})
	stop := false
	go func() {
		r.activeMu.Lock()
		for r.activeCount > 0 && !stop {
			r.activeCond.Wait()
		}
		r.activeMu.Unlock()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		r.activeMu.Lock()
		stop = true
		r.activeCond.Broadcast()
		r.activeMu.Unlock()
		<-done // wait for inner goroutine to finish before returning
		return ctx.Err()
	}
}

// IsDraining reports whether Drain has been called.
func (r *Runner) IsDraining() bool {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	return r.draining
}

// shouldRefuseStart returns true if a new RunJob should refuse to start.
func (r *Runner) shouldRefuseStart() bool {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	return r.draining
}

func (r *Runner) markStart() {
	r.activeMu.Lock()
	r.activeCount++
	r.activeMu.Unlock()
}

func (r *Runner) markFinish() {
	r.activeMu.Lock()
	r.activeCount--
	if r.activeCount <= 0 {
		r.activeCount = 0
		r.activeCond.Broadcast()
	}
	r.activeMu.Unlock()
}

// Breaker returns the per-destination circuit breaker.
func (r *Runner) Breaker() *Breaker { return r.breaker }

// Status returns a snapshot of the currently running backup/restore, or
// an inactive status if nothing is running. Safe to call concurrently.
func (r *Runner) Status() RunStatus {
	r.statusMu.RLock()
	var s RunStatus
	if r.currentRun != nil {
		s = *r.currentRun
	}
	r.statusMu.RUnlock()

	r.queueMu.Lock()
	if len(r.queue) > 0 {
		s.Queue = make([]QueueEntry, len(r.queue))
		copy(s.Queue, r.queue)
	}
	r.queueMu.Unlock()

	return s
}

// CancelJob cancels the currently running job if it matches the given ID.
// Returns an error if no job is running or the running job has a different ID.
func (r *Runner) CancelJob(jobID int64) error {
	r.statusMu.RLock()
	active := r.currentRun != nil && r.currentRun.Active
	var runningID int64
	if r.currentRun != nil {
		runningID = r.currentRun.JobID
	}
	r.statusMu.RUnlock()

	if !active {
		return fmt.Errorf("no job is currently running")
	}
	if runningID != jobID {
		return fmt.Errorf("job %d is not running (running: %d)", jobID, runningID)
	}

	r.cancelMu.Lock()
	fn := r.cancelFn
	r.cancelMu.Unlock()

	if fn == nil {
		return fmt.Errorf("job %d cannot be cancelled", jobID)
	}

	log.Printf("runner: cancelling job %d", jobID)
	fn()

	r.statusMu.Lock()
	if r.currentRun != nil {
		r.currentRun.Cancelling = true
	}
	r.statusMu.Unlock()

	r.broadcast(map[string]any{
		"type":   "job_cancelling",
		"job_id": jobID,
	})

	return nil
}

func (r *Runner) setRunStatus(s *RunStatus) {
	r.statusMu.Lock()
	r.currentRun = s
	r.statusMu.Unlock()
}

func (r *Runner) updateRunProgress(done, failed int, currentItem string) {
	r.statusMu.Lock()
	if r.currentRun != nil {
		r.currentRun.ItemsDone = done
		r.currentRun.ItemsFailed = failed
		r.currentRun.CurrentItem = currentItem
		if currentItem == "" {
			r.currentRun.CurrentItemType = ""
			r.currentRun.CurrentItemPercent = 0
			r.currentRun.CurrentItemMessage = ""
		}
	}
	r.statusMu.Unlock()
}

func (r *Runner) updateCurrentItemProgress(itemType string, percent int, message string) {
	r.statusMu.Lock()
	if r.currentRun != nil {
		r.currentRun.CurrentItemType = itemType
		r.currentRun.CurrentItemPercent = percent
		r.currentRun.CurrentItemMessage = message
	}
	r.statusMu.Unlock()
}

func (r *Runner) reportRestoreProgress(reporter restoreProgressReporter, percent int, message string) {
	if reporter.JobID == 0 && reporter.RunID == 0 && reporter.ItemName == "" {
		return
	}

	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	r.updateCurrentItemProgress(reporter.ItemType, percent, message)
	r.broadcast(map[string]any{
		"type":         "restore_progress",
		"job_id":       reporter.JobID,
		"run_id":       reporter.RunID,
		"item":         reporter.ItemName,
		"item_type":    reporter.ItemType,
		"items_done":   reporter.ItemsDone,
		"items_failed": reporter.ItemsFailed,
		"items_total":  reporter.ItemsTotal,
		"percent":      percent,
		"message":      message,
	})
}

func scaleRestorePhaseProgress(phaseStart, phaseEnd int, completed, total int64) int {
	if phaseEnd <= phaseStart {
		return phaseEnd
	}
	if total <= 0 {
		return phaseEnd
	}
	if completed < 0 {
		completed = 0
	}
	if completed > total {
		completed = total
	}

	span := phaseEnd - phaseStart
	progress := phaseStart + int((completed*int64(span))/total)
	if completed > 0 && progress == phaseStart {
		return phaseStart + 1
	}
	return progress
}

type countingReader struct {
	reader io.Reader
	onRead func(int64)
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && r.onRead != nil {
		r.onRead(int64(n))
	}
	return n, err
}

// heartbeatAdapter wraps a storage.Adapter so every byte streamed through
// Write refreshes the stall watchdog. The dedup backup path uploads pack
// files from inside the handler's chunk walk, where the runner's per-file
// progress callback can be hours apart for a single huge file — without
// byte-level heartbeats those healthy uploads were vulnerable to the same
// false stall-cancel fixed for classic uploads (issue #110).
type heartbeatAdapter struct {
	storage.Adapter
	beat func(int64)
}

func (h *heartbeatAdapter) Write(p string, reader io.Reader) error {
	return h.Adapter.Write(p, &countingReader{reader: reader, onRead: h.beat})
}

// SetSnapshotManager sets the snapshot manager used to persist the database
// to the cache drive after successful backup jobs.
func (r *Runner) SetSnapshotManager(sm *db.SnapshotManager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshotManager = sm
}

// SetEvaluator wires an anomaly evaluator into the runner. After each
// completed run (success, failure, partial, cancelled, or panic), the
// runner calls e.EnqueueRun(runID) so the anomaly engine can inspect the
// run asynchronously. The call is non-blocking by contract (the real
// *anomaly.Evaluator.EnqueueRun uses a buffered channel with drop-oldest
// semantics). Passing nil disables anomaly evaluation (the default).
func (r *Runner) SetEvaluator(e anomalyEnqueuer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evaluator = e
}

// runFinalizationStep runs a post-backup finalization step with a hard
// timeout so a single wedged step (e.g. a hung USB flush, dead remote
// storage, or stuck notify helper) cannot hold the run-slot lock — and
// thus block every future job — indefinitely. On timeout it cancels the
// context handed to the step — steps that hold a storage adapter abort
// their in-flight I/O via context.AfterFunc(ctx, CloseAdapter) and the
// sweep loops check ctx between items, so the goroutine exits instead of
// leaking — logs a clear ERROR naming the step, and returns without
// waiting further. Components used by steps (SnapshotManager via its own
// mutex, the DB pool, per-job retention) are safe for this.
func (r *Runner) runFinalizationStep(name string, jobID, runID int64, timeout time.Duration, fn func(ctx context.Context)) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); fn(ctx) }()
	select {
	case <-done:
		return
	case <-time.After(timeout):
		cancel()
		log.Printf("ERROR runner: finalization step %q for job %d run %d exceeded %s; cancelling the step and continuing so the run slot is not held indefinitely (issue #112)", name, jobID, runID, timeout)
	}
}

// RunJob executes a backup for the given job ID. It is safe to call from
// a goroutine. It creates the job_run record, performs backups for each item,
// updates progress via WebSocket, and creates restore points on success.
//
// Scheduled-cron callers use this entry point. Manual "run now" callers
// should use RunJobManual to suppress retry scheduling; the retry watcher
// (Task 8) uses RunJobRetry to fire a delayed retry of a previously
// failed run.
func (r *Runner) RunJob(jobID int64) {
	r.runJobInternal(jobID, runOptions{})
}

// RunJobManual is invoked by the API "run now" button. Manual runs are
// NOT retried automatically — the user can re-press the button.
func (r *Runner) RunJobManual(jobID int64) {
	r.runJobInternal(jobID, runOptions{manual: true})
}

// RunJobRetry executes a retry of a previously-failed run. The new
// job_run row records retry_of_run_id and retry_attempt so the history
// view can group the retry chain.
func (r *Runner) RunJobRetry(jobID, originalRunID int64, attempt int) {
	r.runJobInternal(jobID, runOptions{retryOfRunID: originalRunID, retryAttempt: attempt})
}

// runJobInternal is the workhorse executed by RunJob/RunJobManual/RunJobRetry.
// opts carries auxiliary state (retry attempt, manual flag) that affects
// run-row population and retry scheduling.
func (r *Runner) runJobInternal(jobID int64, opts runOptions) {
	if r.shouldRefuseStart() {
		log.Printf("runner: refusing to start job %d — daemon is draining", jobID)
		return
	}
	r.markStart()
	defer r.markFinish()

	// Look up the job name before queuing so the queue entry is informative.
	jobName := fmt.Sprintf("Job #%d", jobID)
	if j, err := r.db.GetJob(jobID); err == nil {
		jobName = j.Name
	}

	entry := QueueEntry{
		JobID:    jobID,
		JobName:  jobName,
		QueuedAt: time.Now().Format(time.RFC3339),
	}
	r.queueMu.Lock()
	r.queue = append(r.queue, entry)
	r.queueMu.Unlock()

	r.broadcastQueueUpdate()

	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove ourselves from the queue now that we hold the lock.
	r.queueMu.Lock()
	for i, e := range r.queue {
		if e.JobID == jobID && e.QueuedAt == entry.QueuedAt {
			r.queue = append(r.queue[:i], r.queue[i+1:]...)
			break
		}
	}
	r.queueMu.Unlock()

	r.broadcastQueueUpdate()

	job, err := r.db.GetJob(jobID)
	if err != nil {
		log.Printf("runner: failed to get job %d: %v", jobID, err)
		return
	}

	items, err := r.db.GetJobItems(jobID)
	if err != nil {
		log.Printf("runner: failed to get items for job %d: %v", jobID, err)
		return
	}

	dest, err := r.db.GetStorageDestination(job.StorageDestID)
	if err != nil {
		log.Printf("runner: failed to get storage for job %d: %v", jobID, err)
		return
	}

	// Circuit breaker: if open for this destination, skip the run.
	if r.breaker.IsOpen(dest) {
		log.Printf("runner: breaker open for dest %d (%s) — skipping job %d (%s)",
			dest.ID, dest.Name, jobID, job.Name)
		skipped := db.JobRun{
			JobID:        job.ID,
			Status:       "skipped",
			BackupType:   r.resolveBackupType(job).BackupType,
			RetryAttempt: opts.retryAttempt,
			RetryOfRunID: opts.retryOfRunIDPtr(),
		}
		runID, createErr := r.db.CreateJobRun(skipped)
		if createErr != nil {
			log.Printf("runner: failed to create breaker-skipped run for job %d: %v", jobID, createErr)
			return
		}
		skipped.ID = runID
		openedAt := ""
		if dest.BreakerOpenedAt != nil {
			openedAt = dest.BreakerOpenedAt.Format(time.RFC3339)
		}
		skipped.Log = fmt.Sprintf("Breaker open for destination %q since %s", dest.Name, openedAt)
		// Breaker-open skip: do NOT schedule retry. The breaker exists
		// precisely to avoid hammering an unhealthy destination.
		if updErr := r.db.UpdateJobRun(skipped); updErr != nil {
			log.Printf("runner: failed to update breaker-skipped run %d: %v", runID, updErr)
		}
		r.broadcast(map[string]any{
			"type":   "job_run_completed",
			"job_id": jobID,
			"run_id": runID,
			"status": "skipped",
		})
		return
	}

	// Pre-flight: verify the destination is reachable before opening any
	// backup pipeline. A flaky destination should not tie up the run slot.
	// Failure records a "skipped" run and returns
	// early so retry policy can pick it up on the next tick (Task 7).
	if pfErr := Preflight(context.Background(), dest); pfErr != nil {
		log.Printf("runner: preflight failed for job %d (%s) -> dest %s: %v",
			jobID, job.Name, dest.Name, pfErr)
		skippedRun := db.JobRun{
			JobID:        job.ID,
			Status:       "skipped",
			BackupType:   r.resolveBackupType(job).BackupType,
			ItemsTotal:   0,
			RetryAttempt: opts.retryAttempt,
			RetryOfRunID: opts.retryOfRunIDPtr(),
		}
		runID, createErr := r.db.CreateJobRun(skippedRun)
		if createErr != nil {
			log.Printf("runner: failed to create skipped run for job %d: %v", jobID, createErr)
			return
		}
		skippedRun.ID = runID
		skippedRun.Log = fmt.Sprintf("Pre-flight check failed: %v", pfErr)
		// Preflight failure is potentially transient (DNS blip, mount
		// remount, etc.) — schedule a retry per policy.
		r.scheduleRetryIfDue(&skippedRun, job, dest, opts)
		if updErr := r.db.UpdateJobRun(skippedRun); updErr != nil {
			log.Printf("runner: failed to update skipped run %d: %v", runID, updErr)
		}
		r.broadcast(map[string]any{
			"type":   "job_run_completed",
			"job_id": jobID,
			"run_id": runID,
			"status": "skipped",
		})
		r.breaker.RecordFailure(r.db, dest)
		return
	}

	// Resolve the actual backup type for this run (full/incremental/differential).
	btResult := r.resolveBackupType(job)

	// Stale-item detection (#119). Items whose backing container/VM/folder/
	// plugin/dataset no longer exists are SKIPPED (not failed) and flagged in
	// the DB for user-triggered remediation — Vault never auto-removes them.
	// This runs BEFORE the run record is created so ItemsTotal and the
	// job_run_started broadcast reflect the items we actually process.
	// originalItemCount and staleNames are captured outside the stale-detection
	// block so the all-items-missing guard below (#138) can report how many
	// targets were expected and which ones vanished.
	originalItemCount := len(items)
	var staleNames []string
	{
		inv := engine.GatherInventory()
		var staleIDs, reappearedIDs []int64
		var staleInfo []map[string]any
		kept := items[:0]
		for _, item := range items {
			var settings map[string]any
			if err := json.Unmarshal([]byte(item.Settings), &settings); err != nil {
				log.Printf("runner: job %d: item %d malformed settings JSON: %v", jobID, item.ID, err)
			}
			status := inv.Status(item.ItemType, item.ItemName, settings)
			if status == engine.StatusMissing {
				staleIDs = append(staleIDs, item.ID)
				staleNames = append(staleNames, item.ItemName)
				staleInfo = append(staleInfo, map[string]any{
					"item_id":   item.ID,
					"item_type": item.ItemType,
					"item_name": item.ItemName,
				})
				log.Printf("runner: job %d: stale %s item %q skipped (no longer exists)", jobID, item.ItemType, item.ItemName)
				continue
			}
			// Only clear the missing flag when the item is confirmed PRESENT.
			// StatusUnknown (e.g. Docker/libvirt temporarily down) must leave the
			// flag untouched so a transient outage can't clear a real stale mark.
			if status == engine.StatusPresent && item.MissingSince != nil {
				reappearedIDs = append(reappearedIDs, item.ID)
			}
			kept = append(kept, item)
		}
		items = kept
		if len(staleIDs) > 0 {
			if err := r.db.MarkJobItemsMissing(staleIDs, time.Now().UTC().Format(time.RFC3339)); err != nil {
				log.Printf("runner: job %d: mark stale items: %v", jobID, err)
			}
			if err := notify.Send("Vault",
				fmt.Sprintf("Backup job %q has %d missing item(s)", job.Name, len(staleIDs)),
				"Some backed-up items no longer exist on this server and were skipped. Open Vault → Jobs to review and remove them.",
				notify.ImportanceWarning); err != nil {
				log.Printf("runner: job %d: stale notify: %v", jobID, err)
			}
			r.broadcast(map[string]any{
				"type":   "stale_items_detected",
				"job_id": jobID,
				"count":  len(staleIDs),
				"items":  staleInfo,
			})
		}
		if len(reappearedIDs) > 0 {
			if err := r.db.ClearJobItemsMissing(reappearedIDs); err != nil {
				log.Printf("runner: job %d: clear stale flags: %v", jobID, err)
			}
		}
	}

	// #138 — if every configured item is stale (its backing container/VM/
	// folder/plugin/dataset no longer exists on this server) there is nothing
	// left to back up. Previously the job proceeded with an empty item list and
	// reported "completed" with zero items, masking the fact that the backup
	// targets are gone. Fail the run instead so the operator is alerted and the
	// reliability detector sees the failure. This is NOT a transient/destination
	// problem, so we do not schedule a retry or trip the circuit breaker — the
	// fix is for the operator to re-add or remove the missing targets.
	if len(items) == 0 && len(staleNames) > 0 {
		errMsg := fmt.Sprintf(
			"All %d configured backup target(s) are missing from this server and were skipped: %s. Nothing was backed up.",
			originalItemCount, strings.Join(staleNames, ", "))
		log.Printf("runner: job %d: %s", jobID, errMsg)
		failed := db.JobRun{
			JobID:        job.ID,
			Status:       "failed",
			BackupType:   btResult.BackupType,
			ItemsTotal:   originalItemCount,
			ItemsFailed:  originalItemCount,
			RetryAttempt: opts.retryAttempt,
			RetryOfRunID: opts.retryOfRunIDPtr(),
		}
		runID, createErr := r.db.CreateJobRun(failed)
		if createErr != nil {
			log.Printf("runner: failed to create all-stale failed run for job %d: %v", jobID, createErr)
			return
		}
		failed.ID = runID
		failed.Log = errMsg
		if updErr := r.db.UpdateJobRun(failed); updErr != nil {
			log.Printf("runner: failed to update all-stale failed run %d: %v", runID, updErr)
		}
		r.logActivity("error", "backup",
			fmt.Sprintf("Backup failed: %s", job.Name),
			structuredDetails(map[string]any{
				"job_id": jobID, "run_id": runID, "error": errMsg,
				"missing_items": staleNames,
			}))
		r.broadcast(map[string]any{
			"type":   "job_run_completed",
			"job_id": jobID,
			"run_id": runID,
			"status": "failed",
		})
		if r.evaluator != nil {
			r.evaluator.EnqueueRun(runID)
		}
		return
	}

	run := db.JobRun{
		JobID:        job.ID,
		Status:       "running",
		BackupType:   btResult.BackupType,
		ItemsTotal:   len(items),
		RetryAttempt: opts.retryAttempt,
		RetryOfRunID: opts.retryOfRunIDPtr(),
	}
	runID, err := r.db.CreateJobRun(run)
	if err != nil {
		log.Printf("runner: failed to create job run for job %d: %v", jobID, err)
		return
	}
	run.ID = runID

	// Create a cancellable context with NO wall-clock timeout (issue #110).
	// A backup that is actively transferring data must never be killed by an
	// elapsed-time cap — a legitimate initial backup over a slow WAN link can
	// run for many hours. The no-progress stall watchdog below is the sole
	// automatic canceller (it fires only when zero bytes flow for
	// stallCancelTimeout), and CancelJob() triggers the stored cancel func on
	// demand — per-request stall detection rather than a total-job deadline.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.cancelMu.Lock()
	r.cancelFn = cancel
	r.cancellingJobID = jobID
	r.cancelMu.Unlock()

	// Clear the cancel function when we're done.
	defer func() {
		r.cancelMu.Lock()
		r.cancelFn = nil
		r.cancellingJobID = 0
		r.cancelMu.Unlock()
	}()

	// Recover from panics so the run is marked failed instead of staying "running" forever.
	defer func() {
		r.setRunStatus(nil)
		if rec := recover(); rec != nil {
			log.Printf("runner: PANIC during job %d run %d: %v", jobID, runID, rec)
			run.Status = "failed"
			run.Log = fmt.Sprintf("Internal error (panic): %v", rec)
			// Panic is potentially transient; schedule a retry. The
			// breaker check inside scheduleRetryIfDue prevents storms
			// against a broken destination.
			r.scheduleRetryIfDue(&run, job, dest, opts)
			if updateErr := r.db.UpdateJobRun(run); updateErr != nil {
				log.Printf("runner: failed to mark panicked run %d as failed: %v", runID, updateErr)
			}
			if freshDest, ferr := r.db.GetStorageDestination(dest.ID); ferr == nil {
				r.breaker.RecordFailure(r.db, freshDest)
			}
			r.broadcast(map[string]any{
				"type":   "job_run_completed",
				"job_id": jobID,
				"run_id": runID,
				"status": "failed",
			})
			// Trigger anomaly evaluation even for panicked runs; the
			// reliability detector needs to know about all failures.
			// EnqueueRun is non-blocking by contract.
			if r.evaluator != nil {
				r.evaluator.EnqueueRun(runID)
			}
		}
	}()

	r.broadcast(map[string]any{
		"type":        "job_run_started",
		"job_id":      jobID,
		"run_id":      runID,
		"job_name":    job.Name,
		"run_type":    "backup",
		"items_total": len(items),
	})

	r.setRunStatus(&RunStatus{
		Active:     true,
		JobID:      jobID,
		RunID:      runID,
		JobName:    job.Name,
		RunType:    "backup",
		ItemsTotal: len(items),
		StartedAt:  time.Now().Format(time.RFC3339),
	})

	// Initialize progress tracking for stall detection.
	r.lastProgressMu.Lock()
	r.lastProgress = time.Now()
	r.lastProgressMu.Unlock()

	// Start a stall detector goroutine. It warns when no progress is received
	// for stallWarnInterval, and cancels the job after stallCancelTimeout.
	r.startStallWatchdog(ctx, cancel, jobID, 5*time.Minute, stallWarnInterval, stallCancelTimeout)

	startedDetails := map[string]any{
		"job_id": jobID, "run_id": runID, "items": len(items),
		"items_list":  jobItemNames(items),
		"job_name":    job.Name,
		"backup_type": btResult.BackupType,
		"destination": dest.Name,
	}
	if job.Schedule != "" {
		startedDetails["schedule"] = job.Schedule
	}
	r.logActivity("info", "backup", fmt.Sprintf("Backup started: %s", job.Name),
		structuredDetails(startedDetails))

	timestamp := time.Now().Format("2006-01-02_150405")
	basePath := fmt.Sprintf("%s/%d_%s", job.Name, runID, timestamp)

	// Resolve encryption passphrase if job has encryption enabled.
	var encryptPassphrase string
	if job.Encryption == "age" {
		encryptPassphrase = r.resolvePassphrase()
		if encryptPassphrase == "" {
			log.Printf("runner: job %d has encryption=age but no passphrase configured", jobID)
			run.Status = "failed"
			run.Log = "Encryption enabled but no passphrase configured in settings\n"
			r.scheduleRetryIfDue(&run, job, dest, opts)
			_ = r.db.UpdateJobRun(run)
			return
		}
	}

	// Execute pre-script if configured.
	if job.PreScript != "" {
		log.Printf("runner: job %d executing pre-script: %s", jobID, job.PreScript)
		output, err := runScript(job.PreScript, defaultScriptTimeout)
		if output != "" {
			log.Printf("runner: pre-script output: %s", strings.TrimSpace(output))
		}
		if err != nil {
			log.Printf("runner: job %d pre-script failed: %v", jobID, err)
			run.Status = "failed"
			run.Log = fmt.Sprintf("Pre-script failed: %v\nOutput: %s", err, output)
			r.scheduleRetryIfDue(&run, job, dest, opts)
			_ = r.db.UpdateJobRun(run)
			r.logActivity("error", "backup",
				fmt.Sprintf("Pre-script failed: %s", job.Name),
				structuredDetails(map[string]any{
					"job_id": jobID, "run_id": runID, "script": job.PreScript, "error": err.Error(),
				}))
			return
		}
	}

	var (
		totalSize     int64
		itemsDone     int
		itemsFailed   int
		itemResults   []map[string]any
		itemChecksums = make(map[string]map[string]string)
		vmCheckpoints = make(map[string]string)
		// itemManifests holds per-item dedup manifest IDs (hex-encoded) for
		// the dedup path. Populated when result.Meta["manifest_id"] is set
		// by backupItemChunked. Persisted in restore_point metadata as
		// "item_manifests" so the restore path can resolve manifests per
		// item even in multi-item jobs.
		itemManifests = make(map[string]string)
		failedNames   []string
		jobStart      = time.Now()
	)

	// Resolve container IDs by name — stored IDs may be stale after
	// container updates, reboots, or Docker Compose recreates.
	resolvedIDs := make(map[string]string) // item_name -> current container ID
	if hasContainerItems(items) {
		ch, err := engine.NewContainerHandler()
		if err == nil {
			if current, err := ch.ListItems(); err == nil {
				byName := make(map[string]string, len(current))
				for _, c := range current {
					if id, ok := c.Settings["id"].(string); ok {
						byName[c.Name] = id
					}
				}
				for _, item := range items {
					if item.ItemType == "container" {
						if currentID, ok := byName[item.ItemName]; ok && currentID != item.ItemID {
							log.Printf("runner: container %q: ID changed from %s to %s (resolved by name)",
								item.ItemName, item.ItemID[:12], currentID[:12])
							resolvedIDs[item.ItemName] = currentID
						}
					}
				}
			}
		}
	}

	// stop_all mode: stop all container items up-front, set no_stop so the
	// individual handler skips stop/start, then restart them all after the loop.
	var stoppedContainerIDs []string
	if job.ContainerMode == "stop_all" {
		var containerIDs []string
		for _, item := range items {
			if item.ItemType == "container" {
				id := item.ItemID
				if resolved, ok := resolvedIDs[item.ItemName]; ok {
					id = resolved
				}
				containerIDs = append(containerIDs, id)
			}
		}
		if len(containerIDs) > 0 {
			r.broadcast(map[string]any{
				"type":   "containers_stopping_all",
				"job_id": jobID,
				"count":  len(containerIDs),
			})
			stopped, err := engine.StopContainers(containerIDs)
			stoppedContainerIDs = stopped
			if err != nil {
				log.Printf("runner: stop_all failed for job %d: %v (stopped %d of %d)",
					jobID, err, len(stopped), len(containerIDs))
			}
		}
	}

	// Deferred remote upload mode (#77): stage every item locally first, restart
	// stop_all containers as soon as staging finishes, then upload to remote
	// storage in a second phase. Local destinations always run inline.
	// Dedup-enabled destinations also run inline so the chunked backup path
	// is taken in backupItem rather than the classic stage-then-upload split.
	deferred := job.DeferRemoteUpload && dest.Type != "local" && !dest.DedupEnabled
	type stagedItemEntry struct {
		engineItem engine.BackupItem
		dbItem     db.JobItem
		itemPath   string
		tmpDir     string
		cleanup    func()
		result     *engine.BackupResult
	}
	var stagedItems []stagedItemEntry
	defer func() {
		for _, s := range stagedItems {
			if s.cleanup != nil {
				s.cleanup()
			}
		}
	}()

	for _, item := range items {
		// Check for cancellation between items.
		if ctx.Err() != nil {
			log.Printf("runner: job %d cancelled between items", jobID)
			break
		}

		var settings map[string]any
		if err := json.Unmarshal([]byte(item.Settings), &settings); err != nil {
			settings = make(map[string]any)
		}

		itemID := item.ItemID
		if resolved, ok := resolvedIDs[item.ItemName]; ok {
			itemID = resolved
		}

		backupItem := engine.BackupItem{
			Name: item.ItemName,
			Type: item.ItemType,
			Settings: map[string]any{
				"id":    itemID,
				"image": settings["image"],
				"state": settings["state"],
			},
			Compression: job.Compression,
		}

		if item.ItemType == "container" {
			backupItem.Settings["container_mode"] = job.ContainerMode
			if job.ContainerMode == "stop_all" {
				backupItem.Settings["no_stop"] = true
			}
			if ep, ok := settings["exclude_paths"]; ok {
				backupItem.Settings["exclude_paths"] = ep
			}
			if em, ok := settings["excluded_mounts"]; ok {
				backupItem.Settings["excluded_mounts"] = em
			}
		}

		// VM items need the backup mode (snapshot or cold).
		if item.ItemType == "vm" {
			for key, value := range settings {
				backupItem.Settings[key] = value
			}
			backupItem.Settings["id"] = itemID
			backupItem.Settings["backup_mode"] = job.VMMode
			backupItem.Settings["backup_type"] = btResult.BackupType
			backupItem.Settings["backup_run_id"] = runID
			// For incremental/differential, read the parent libvirt checkpoint
			// from the parent restore point metadata. The engine falls back
			// to a full backup if the named checkpoint no longer exists.
			if btResult.ParentRP != nil && btResult.BackupType != "full" {
				if cp := vmCheckpointFromRPMeta(btResult.ParentRP.Metadata, item.ItemName); cp != "" {
					backupItem.Settings["parent_checkpoint"] = cp
				}
			}
		}

		if btResult.ParentRP != nil && (item.ItemType == "container" || item.ItemType == "folder") {
			backupItem.Settings["changed_since"] = btResult.ParentRP.CreatedAt.Format(time.RFC3339)
		}

		// Folder items need the path from settings.
		if item.ItemType == "folder" {
			backupItem.Settings["path"] = settings["path"]
			backupItem.Settings["preset"] = settings["preset"]
		}

		// ZFS items need the dataset and related metadata from settings.
		if item.ItemType == "zfs" {
			for key, value := range settings {
				backupItem.Settings[key] = value
			}
			backupItem.Settings["backup_type"] = btResult.BackupType
			// For incremental ZFS backups, read the parent snapshot from the
			// previous restore point's zfs_meta.json sidecar.
			if btResult.ParentRP != nil && btResult.BackupType != "full" {
				var parentMeta map[string]any
				if json.Unmarshal([]byte(btResult.ParentRP.Metadata), &parentMeta) == nil {
					if ps, ok := parentMeta["zfs_snapshot"].(string); ok && ps != "" {
						backupItem.Settings["parent_snapshot"] = ps
					}
				}
			}
		}

		itemPath := filepath.Join(basePath, item.ItemName)

		r.broadcast(map[string]any{
			"type":        "item_backup_start",
			"job_id":      jobID,
			"run_id":      runID,
			"item_name":   item.ItemName,
			"item_type":   item.ItemType,
			"items_total": len(items),
			"items_done":  itemsDone,
		})

		r.updateRunProgress(itemsDone, itemsFailed, item.ItemName)
		r.updateCurrentItemProgress(item.ItemType, 0, "Starting...")

		if deferred {
			r.broadcast(map[string]any{
				"type":      "backup_phase",
				"job_id":    jobID,
				"run_id":    runID,
				"phase":     "staging",
				"item_name": item.ItemName,
				"item_type": item.ItemType,
			})
			tmpDir, stageResult, cleanup, stageErr := r.stageItemLocally(ctx, backupItem, dest)
			if stageErr != nil {
				if ctx.Err() != nil {
					log.Printf("runner: job %d item %s cancelled during staging: %v", jobID, item.ItemName, stageErr)
					break
				}
				itemsFailed++
				failedNames = append(failedNames, item.ItemName)
				itemResults = append(itemResults, map[string]any{
					"name":   item.ItemName,
					"status": "failed",
					"error":  stageErr.Error(),
				})
				log.Printf("runner: stage item %s failed: %v", item.ItemName, stageErr)
				r.broadcast(map[string]any{
					"type":        "item_backup_failed",
					"job_id":      jobID,
					"run_id":      runID,
					"item_name":   item.ItemName,
					"item_type":   item.ItemType,
					"items_total": len(items),
					"items_done":  itemsDone,
					"error":       stageErr.Error(),
				})
			} else {
				stagedItems = append(stagedItems, stagedItemEntry{
					engineItem: backupItem,
					dbItem:     item,
					itemPath:   itemPath,
					tmpDir:     tmpDir,
					cleanup:    cleanup,
					result:     stageResult,
				})
				r.broadcast(map[string]any{
					"type":        "item_staged",
					"job_id":      jobID,
					"run_id":      runID,
					"item_name":   item.ItemName,
					"item_type":   item.ItemType,
					"items_total": len(items),
				})
			}
			_ = r.db.UpdateJobRunProgress(runID, itemsDone, itemsFailed, totalSize)
			r.updateRunProgress(itemsDone, itemsFailed, "")
			continue
		}

		result, checksums, backupErr := r.backupItem(ctx, backupItem, dest, itemPath, job.VerifyBackup, encryptPassphrase, job.Compression, job.EffectiveUploadConcurrency())
		if backupErr != nil {
			// If the context was cancelled, stop processing remaining items.
			if ctx.Err() != nil {
				log.Printf("runner: job %d item %s cancelled: %v", jobID, item.ItemName, backupErr)
				break
			}
			itemsFailed++
			failedNames = append(failedNames, item.ItemName)
			itemResults = append(itemResults, map[string]any{
				"name":   item.ItemName,
				"status": "failed",
				"error":  backupErr.Error(),
			})
			log.Printf("runner: backup item %s failed: %v", item.ItemName, backupErr)

			r.broadcast(map[string]any{
				"type":        "item_backup_failed",
				"job_id":      jobID,
				"run_id":      runID,
				"item_name":   item.ItemName,
				"item_type":   item.ItemType,
				"items_total": len(items),
				"items_done":  itemsDone,
				"error":       backupErr.Error(),
			})
		} else {
			itemsDone++
			var itemSize int64
			if result != nil {
				for _, f := range result.Files {
					itemSize += f.Size
				}
			}
			totalSize += itemSize

			resEntry := map[string]any{
				"name":       item.ItemName,
				"status":     "ok",
				"size_bytes": itemSize,
			}
			if job.VerifyBackup {
				resEntry["verified"] = true
			}
			// For ZFS items, capture the snapshot name from the backup settings
			// so it can be referenced by future incremental backups.
			if item.ItemType == "zfs" {
				if snap, ok := backupItem.Settings["dataset"].(string); ok {
					resEntry["zfs_dataset"] = snap
				}
			}
			// For VM items, capture the libvirt checkpoint name produced by
			// the engine so future incremental/differential backups can
			// reference it as their parent.
			if item.ItemType == "vm" && result != nil {
				if cp, ok := result.Meta["vm_checkpoint"].(string); ok && cp != "" {
					vmCheckpoints[item.ItemName] = cp
					resEntry["vm_checkpoint"] = cp
				}
				if bt, ok := result.Meta["vm_backup_type"].(string); ok && bt != "" {
					resEntry["vm_backup_type"] = bt
				}
			}
			itemResults = append(itemResults, resEntry)

			// Store checksums per item for restore-point metadata.
			if len(checksums) > 0 {
				itemChecksums[item.ItemName] = checksums
			}

			// Capture dedup manifest_id (set by backupItemChunked) so the
			// restore-point row can be linked to the chunked manifest.
			if result != nil {
				if mid, ok := result.Meta["manifest_id"].([]byte); ok && len(mid) == 32 {
					itemManifests[item.ItemName] = hex.EncodeToString(mid)
				}
			}

			r.broadcast(map[string]any{
				"type":        "item_backup_done",
				"job_id":      jobID,
				"run_id":      runID,
				"item_name":   item.ItemName,
				"item_type":   item.ItemType,
				"items_total": len(items),
				"items_done":  itemsDone,
				"size_bytes":  itemSize,
				"verified":    job.VerifyBackup,
			})

			// After successful backup of a container item (per-item mode), verify health.
			// In per-item mode the engine handler restarts each container after backup.
			if item.ItemType == "container" && job.ContainerMode != "stop_all" {
				go func(itemID, itemName string) {
					result, err := engine.VerifyContainerHealth(itemID, itemName, 60*time.Second)
					if err != nil {
						log.Printf("runner: health check error for %s: %v", itemName, err)
						return
					}
					r.broadcast(map[string]any{
						"type":        "container_health_check",
						"job_id":      jobID,
						"name":        result.ContainerName,
						"status":      result.Status,
						"message":     result.Message,
						"duration_ms": result.Duration.Milliseconds(),
					})
					hcLevel := "info"
					if result.Status != "healthy" {
						hcLevel = "warn"
					}
					r.logActivity(hcLevel, "health",
						fmt.Sprintf("Health check: %s", result.ContainerName),
						structuredDetails(map[string]any{
							"job_id":         jobID,
							"run_id":         runID,
							"container_name": result.ContainerName,
							"status":         result.Status,
							"message":        result.Message,
							"duration_ms":    result.Duration.Milliseconds(),
						}))
				}(item.ItemID, item.ItemName)
			}
		}

		// Update in-flight progress so the UI reflects real-time counts.
		_ = r.db.UpdateJobRunProgress(runID, itemsDone, itemsFailed, totalSize)
		r.updateRunProgress(itemsDone, itemsFailed, "")
	}

	// stop_all mode: restart all containers that were stopped before the loop.
	if len(stoppedContainerIDs) > 0 {
		r.broadcast(map[string]any{
			"type":   "containers_restarting_all",
			"job_id": jobID,
			"count":  len(stoppedContainerIDs),
		})
		if errs := engine.StartContainers(stoppedContainerIDs); len(errs) > 0 {
			for _, e := range errs {
				log.Printf("runner: stop_all restart error for job %d: %v", jobID, e)
			}
		}

		// Verify container health after restarts (informational — does not affect status).
		r.broadcast(map[string]any{
			"type":    "phase_message",
			"job_id":  jobID,
			"message": fmt.Sprintf("Verifying health of %d containers...", len(stoppedContainerIDs)),
		})

		var healthResults []map[string]any
		for _, id := range stoppedContainerIDs {
			// Find the container name from items.
			name := id
			for _, item := range items {
				if item.ItemID == id {
					name = item.ItemName
					break
				}
			}
			result, err := engine.VerifyContainerHealth(id, name, 60*time.Second)
			if err != nil {
				log.Printf("runner: health check error for %s: %v", name, err)
				continue
			}
			healthResults = append(healthResults, map[string]any{
				"name":        result.ContainerName,
				"status":      result.Status,
				"message":     result.Message,
				"duration_ms": result.Duration.Milliseconds(),
			})
			r.broadcast(map[string]any{
				"type":        "container_health_check",
				"job_id":      jobID,
				"name":        result.ContainerName,
				"status":      result.Status,
				"message":     result.Message,
				"duration_ms": result.Duration.Milliseconds(),
			})
		}
		if len(healthResults) > 0 {
			healthy := 0
			for _, hr := range healthResults {
				if hr["status"] == "healthy" {
					healthy++
				}
			}
			r.logActivity("info", "health",
				fmt.Sprintf("Health check: %s", job.Name),
				structuredDetails(map[string]any{
					"summary": map[string]any{
						"job_id":               jobID,
						"run_id":               runID,
						"containers_checked":   len(healthResults),
						"containers_healthy":   healthy,
						"containers_unhealthy": len(healthResults) - healthy,
					},
					"results": healthResults,
				}))
		}
	}

	// Phase 2 of deferred mode: upload all staged items to remote storage.
	// Containers have already been restarted (above) so the upload runs in
	// parallel to normal service. Each item's success/failure is recorded
	// here, mirroring the inline path's accounting.
	if deferred && len(stagedItems) > 0 {
		r.broadcast(map[string]any{
			"type":   "backup_phase",
			"job_id": jobID,
			"run_id": runID,
			"phase":  "uploading",
			"count":  len(stagedItems),
		})
		for _, s := range stagedItems {
			if ctx.Err() != nil {
				log.Printf("runner: job %d cancelled before uploading %s", jobID, s.dbItem.ItemName)
				break
			}
			r.broadcast(map[string]any{
				"type":      "item_upload_start",
				"job_id":    jobID,
				"run_id":    runID,
				"item_name": s.dbItem.ItemName,
				"item_type": s.dbItem.ItemType,
			})
			checksums, uploadErr := r.uploadStagedFilesN(ctx, s.tmpDir, dest, s.itemPath, job.VerifyBackup, encryptPassphrase, job.Compression, s.dbItem.ItemType, s.dbItem.ItemName, job.EffectiveUploadConcurrency())
			if uploadErr != nil {
				if ctx.Err() != nil {
					log.Printf("runner: job %d upload of %s cancelled: %v", jobID, s.dbItem.ItemName, uploadErr)
					break
				}
				itemsFailed++
				failedNames = append(failedNames, s.dbItem.ItemName)
				itemResults = append(itemResults, map[string]any{
					"name":   s.dbItem.ItemName,
					"status": "failed",
					"error":  uploadErr.Error(),
				})
				log.Printf("runner: upload item %s failed: %v", s.dbItem.ItemName, uploadErr)
				r.broadcast(map[string]any{
					"type":        "item_backup_failed",
					"job_id":      jobID,
					"run_id":      runID,
					"item_name":   s.dbItem.ItemName,
					"item_type":   s.dbItem.ItemType,
					"items_total": len(items),
					"items_done":  itemsDone,
					"error":       uploadErr.Error(),
				})
			} else {
				itemsDone++
				var itemSize int64
				if s.result != nil {
					for _, f := range s.result.Files {
						itemSize += f.Size
					}
				}
				totalSize += itemSize

				resEntry := map[string]any{
					"name":       s.dbItem.ItemName,
					"status":     "ok",
					"size_bytes": itemSize,
				}
				if job.VerifyBackup {
					resEntry["verified"] = true
				}
				if s.dbItem.ItemType == "zfs" {
					if snap, ok := s.engineItem.Settings["dataset"].(string); ok {
						resEntry["zfs_dataset"] = snap
					}
				}
				if s.dbItem.ItemType == "vm" && s.result != nil {
					if cp, ok := s.result.Meta["vm_checkpoint"].(string); ok && cp != "" {
						vmCheckpoints[s.dbItem.ItemName] = cp
						resEntry["vm_checkpoint"] = cp
					}
					if bt, ok := s.result.Meta["vm_backup_type"].(string); ok && bt != "" {
						resEntry["vm_backup_type"] = bt
					}
				}
				itemResults = append(itemResults, resEntry)
				if len(checksums) > 0 {
					itemChecksums[s.dbItem.ItemName] = checksums
				}
				r.broadcast(map[string]any{
					"type":        "item_backup_done",
					"job_id":      jobID,
					"run_id":      runID,
					"item_name":   s.dbItem.ItemName,
					"item_type":   s.dbItem.ItemType,
					"items_total": len(items),
					"items_done":  itemsDone,
					"size_bytes":  itemSize,
					"verified":    job.VerifyBackup,
				})
			}
			_ = r.db.UpdateJobRunProgress(runID, itemsDone, itemsFailed, totalSize)
			r.updateRunProgress(itemsDone, itemsFailed, "")
		}
	}

	status := "completed"
	// userCancelled tells us whether the cancellation came from the API
	// (operator action) vs. ctx timeout / stall-detector goroutine.
	// Operator cancels MUST NOT trigger retry; stall/timeout SHOULD.
	userCancelled := false
	if ctx.Err() != nil {
		status = "cancelled"
		r.statusMu.RLock()
		if r.currentRun != nil && r.currentRun.Cancelling {
			userCancelled = true
		}
		r.statusMu.RUnlock()
		if userCancelled {
			log.Printf("runner: job %d was cancelled by user after %v", jobID, time.Since(jobStart).Truncate(time.Second))
			run.Log = "Job was cancelled by user"
		} else {
			// There is no longer a wall-clock deadline (issue #110); the only
			// other automatic canceller is the no-progress stall watchdog.
			log.Printf("runner: job %d cancelled by stall watchdog (no progress for %v) after %v", jobID, stallCancelTimeout, time.Since(jobStart).Truncate(time.Second))
			run.Log = fmt.Sprintf("Job cancelled: no data transferred for %v (stall watchdog)", stallCancelTimeout)
		}
	} else if itemsFailed > 0 && itemsDone == 0 {
		status = "failed"
	} else if itemsFailed > 0 {
		status = "partial"
	}

	// Safety net (#138): items were configured and survived stale detection, yet
	// the run ended up with zero backed-up AND zero failed items. There is no
	// legitimate "completed" outcome here — it means every item was silently
	// skipped (e.g. an unforeseen handler edge case). Report a failure rather
	// than a misleading success with nothing backed up.
	zeroItemsProcessed := status == "completed" && itemsDone == 0 && itemsFailed == 0 && run.ItemsTotal > 0
	if zeroItemsProcessed {
		status = "failed"
	}

	run.Status = status
	if zeroItemsProcessed {
		run.Log = fmt.Sprintf("No items were backed up despite %d item(s) being configured.", run.ItemsTotal)
	} else {
		run.Log = structuredDetails(itemResults)
	}
	run.ItemsDone = itemsDone
	run.ItemsFailed = itemsFailed
	run.SizeBytes = totalSize
	// Schedule a retry on:
	//   - "failed"    (genuine end-of-pipeline failure)
	//   - "cancelled" UNLESS the operator pressed Cancel (stall/timeout
	//     paths set Cancelling=false and benefit from a retry).
	// "completed" / "partial" / user-cancel → no retry.
	switch status {
	case "failed":
		r.scheduleRetryIfDue(&run, job, dest, opts)
	case "cancelled":
		if !userCancelled {
			r.scheduleRetryIfDue(&run, job, dest, opts)
		}
	}
	if err := r.db.UpdateJobRun(run); err != nil {
		log.Printf("runner: failed to update job run %d: %v", runID, err)
	}

	// Fix #112 — clear the live "in progress" dashboard status the moment the
	// run is logically complete and persisted to the DB. The deferred
	// setRunStatus(nil) at the top of this function remains as an idempotent
	// safety net, but clearing here ensures the Dashboard reflects completion
	// even if a slow or stuck finalization step (database backup, snapshot
	// flush, retention enforcement, or notification) delays the function return.
	log.Printf("runner: job %d run %d logically complete (%s); finalizing", jobID, runID, status)
	r.setRunStatus(nil)

	// Record breaker outcome based on final job status. "completed" or
	// "partial" runs count as a destination success (the storage was
	// reachable end-to-end); "failed" counts as a destination failure.
	// "cancelled" is operator action and does not affect the breaker.
	// Re-fetch dest so the breaker sees the latest persisted counters.
	if freshDest, ferr := r.db.GetStorageDestination(dest.ID); ferr == nil {
		switch status {
		case "completed", "partial":
			r.breaker.RecordSuccess(r.db, freshDest)
		case "failed":
			r.breaker.RecordFailure(r.db, freshDest)
		}
	}

	if itemsDone > 0 {
		rpMeta := map[string]any{
			"items":        itemsDone,
			"items_failed": itemsFailed,
			"job_name":     job.Name,
		}
		// Store per-item sizes so the restore wizard can show individual item sizes.
		itemSizes := make(map[string]int64, len(itemResults))
		for _, res := range itemResults {
			name, _ := res["name"].(string)
			size, _ := res["size_bytes"].(int64)
			if name != "" && size > 0 {
				itemSizes[name] = size
			}
		}
		if len(itemSizes) > 0 {
			rpMeta["item_sizes"] = itemSizes
		}
		if job.VerifyBackup && len(itemChecksums) > 0 {
			rpMeta["checksums"] = itemChecksums
			rpMeta["verified"] = true
		}
		rpMeta["backup_type"] = btResult.BackupType
		if btResult.ParentRP != nil {
			rpMeta["parent_restore_point_id"] = btResult.ParentRP.ID
		}
		if len(vmCheckpoints) > 0 {
			rpMeta["vm_checkpoints"] = vmCheckpoints
		}
		// item_manifests carries one hex-encoded dedup manifest ID per item
		// for dedup destinations. The restore path uses this to resolve the
		// per-item manifest within a multi-item restore point. For
		// single-item dedup jobs we additionally set rp.ManifestID (below)
		// so the common case can look up the manifest without parsing JSON.
		if len(itemManifests) > 0 {
			rpMeta["item_manifests"] = itemManifests
		}
		metadata, _ := json.Marshal(rpMeta)

		var parentID int64
		if btResult.ParentRP != nil {
			parentID = btResult.ParentRP.ID
		}
		rp := db.RestorePoint{
			JobRunID:             runID,
			JobID:                jobID,
			BackupType:           btResult.BackupType,
			StoragePath:          basePath,
			Metadata:             string(metadata),
			SizeBytes:            totalSize,
			ParentRestorePointID: parentID,
		}
		// For dedup runs with exactly one item, persist the manifest ID
		// directly on the row so the common case avoids a metadata lookup.
		if len(itemManifests) == 1 {
			for _, hexID := range itemManifests {
				if decoded, err := hex.DecodeString(hexID); err == nil && len(decoded) == 32 {
					rp.ManifestID = decoded
				}
			}
		}
		rpID, err := r.db.CreateRestorePoint(rp)
		if err != nil {
			log.Printf("runner: failed to create restore point for run %d: %v", runID, err)
		} else if rp.ManifestID != nil {
			// CreateRestorePoint may not persist ManifestID depending on
			// the repository's INSERT columns; ensure it's stored via the
			// dedicated setter.
			if err := r.db.SetRestorePointManifestID(rpID, rp.ManifestID); err != nil {
				log.Printf("runner: failed to persist manifest_id for rp %d: %v", rpID, err)
			}
		}

		// Write manifest.json to storage for out-of-band recovery. For dedup
		// destinations we include itemManifests so a re-import on another
		// instance can resolve per-item chunks via the dedup repo.
		// Wrapped with a hard timeout (Fix #112): writeManifest opens its own
		// storage adapter and could hang on a dead remote, which would hold the
		// run-slot mutex indefinitely. The restore point row is already
		// persisted (CreateRestorePoint above), so a bounded/abandoned manifest
		// write only degrades out-of-band recovery, not normal DB-driven restore.
		manifestDest := dest // stable copy for closure
		r.runFinalizationStep("manifest", jobID, runID, finalizeManifestTimeout, func(stepCtx context.Context) {
			r.writeManifest(stepCtx, manifestDest, basePath, job, items, runID, btResult.BackupType, itemsDone, itemsFailed, totalSize, itemChecksums, itemManifests, timestamp)
		})

		// Auto-backup the SQLite database to a centralised storage location.
		// Wrapped with a hard timeout (Fix #112) so a stuck remote write cannot
		// hold the run-slot mutex indefinitely.
		localDest := dest // stable copy for closure
		r.runFinalizationStep("database-backup", jobID, runID, finalizeDBBackupTimeout, func(stepCtx context.Context) {
			r.backupDatabase(stepCtx, localDest)
		})

		// Persist database to cache drive and USB backup after successful backup.
		// r.mu is already held by RunJob (line 289), so access snapshotManager
		// directly without re-locking. Wrapped with a hard timeout (Fix #112).
		sm := r.snapshotManager
		if sm != nil {
			// Local file I/O only — no cancellable seam, so the ctx is unused.
			r.runFinalizationStep("snapshot-usb", jobID, runID, finalizeSnapshotTimeout, func(context.Context) {
				if err := sm.SaveSnapshotAndUSBBackup(); err != nil {
					log.Printf("runner: snapshot save error: %v", err)
				}
			})
		}
	}

	ltr := ltrPolicyFromJob(job)
	if ltr.IsActive() {
		// Wrapped with a hard timeout (Fix #112).
		localDest := dest
		localLTR := ltr
		r.runFinalizationStep("retention-ltr", jobID, runID, finalizeRetentionTimeout, func(stepCtx context.Context) {
			r.enforceRetentionLTR(stepCtx, localDest, jobID, localLTR)
		})
	} else if job.RetentionCount > 0 || job.RetentionDays > 0 {
		// Wrapped with a hard timeout (Fix #112).
		localDest := dest
		keepCount := job.RetentionCount
		keepDays := job.RetentionDays
		r.runFinalizationStep("retention", jobID, runID, finalizeRetentionTimeout, func(stepCtx context.Context) {
			r.enforceRetention(stepCtx, localDest, jobID, keepCount, keepDays)
		})
	}

	// Execute post-script if configured. runScript already enforces
	// defaultScriptTimeout internally — no additional wrapper needed.
	if job.PostScript != "" {
		log.Printf("runner: job %d executing post-script: %s", jobID, job.PostScript)
		output, err := runScript(job.PostScript, defaultScriptTimeout)
		if output != "" {
			log.Printf("runner: post-script output: %s", strings.TrimSpace(output))
		}
		if err != nil {
			log.Printf("runner: job %d post-script failed: %v", jobID, err)
			r.logActivity("warn", "backup",
				fmt.Sprintf("Post-script failed: %s", job.Name),
				structuredDetails(map[string]any{
					"job_id": jobID, "run_id": runID, "script": job.PostScript, "error": err.Error(),
				}))
		}
	}

	// broadcast, EnqueueRun, and logActivity are non-blocking / fast — no
	// timeout wrapper needed.
	r.broadcast(map[string]any{
		"type":         "job_run_completed",
		"job_id":       jobID,
		"run_id":       runID,
		"status":       status,
		"items_done":   itemsDone,
		"items_failed": itemsFailed,
		"size_bytes":   totalSize,
	})

	// Trigger anomaly evaluation for this run. The evaluator inspects the
	// persisted run record asynchronously (on its own worker goroutine) so it
	// sees completed, failed, partial, and cancelled runs alike — the
	// reliability detector in particular needs failure runs to build its
	// signal. EnqueueRun is non-blocking by contract (buffered channel send
	// with drop-oldest semantics), so calling it directly here does not delay
	// the runner.
	if r.evaluator != nil {
		r.evaluator.EnqueueRun(runID)
	}

	log.Printf("runner: job %d run %d finished: %s (done=%d, failed=%d, size=%d)",
		jobID, runID, status, itemsDone, itemsFailed, totalSize)

	r.logActivity(logLevelForStatus(status), "backup",
		fmt.Sprintf("Backup %s: %s", status, job.Name),
		structuredDetails(map[string]any{
			"job_id": jobID, "job_name": job.Name,
			"run_id": runID, "backup_type": btResult.BackupType,
			"destination": dest.Name,
			"items_done":  itemsDone, "items_failed": itemsFailed,
			"size_bytes": totalSize, "duration_seconds": int(time.Since(jobStart).Seconds()),
			"failed_items": failedNames,
		}))

	// Send Unraid + Discord notifications. notify.Send is now bounded (30s
	// exec timeout). Belt-and-braces: wrap the whole sendNotification call in
	// runFinalizationStep so any other delay (Discord HTTP, DB lookup) is also
	// bounded. Captures all needed locals in stable copies.
	localJob := job
	localStatus := status
	localDone := itemsDone
	localFailed := itemsFailed
	localSize := totalSize
	localDurationSec := int(time.Since(jobStart).Seconds())
	localFailedNames := failedNames
	// notify.Send is already bounded internally (30s exec timeout), so the
	// step context is unused here.
	r.runFinalizationStep("notify", jobID, runID, finalizeNotifyTimeout, func(context.Context) {
		r.sendNotification(localJob, localStatus, localDone, localFailed, localSize, localDurationSec, localFailedNames)
	})
}

// newHandler instantiates the engine handler for the given backup item type.
// Shared by the classic (stageItemLocally / restoreStagedItem) and chunked
// (backupItemChunked / restoreSinglePointChunked) paths so all five handler
// types stay registered in one place.
func newHandler(itemType string) (engine.Handler, error) {
	switch itemType {
	case "container":
		return engine.NewContainerHandler()
	case "vm":
		return engine.NewVMHandler()
	case "folder":
		return engine.NewFolderHandler()
	case "plugin":
		return engine.NewPluginHandler()
	case "zfs":
		return engine.NewZFSHandler()
	default:
		return nil, fmt.Errorf("unknown item type: %s", itemType)
	}
}

// openDedupRepo opens or initialises the dedup repo for dest. On first
// backup to a dedup-enabled destination, _vault/repo.json doesn't exist
// yet — Init creates it. Subsequent calls Open the existing one and
// unseal the master key with r.serverKey.
//
// Caller is responsible for storage.CloseAdapter(adapter) when done.
func (r *Runner) openDedupRepo(adapter storage.Adapter, dest db.StorageDestination) (*dedup.Repo, error) {
	if _, err := adapter.Stat("_vault/repo.json"); err == nil {
		return dedup.OpenRepo(r.db, adapter, dest.ID, r.serverKey)
	}
	return dedup.InitRepo(r.db, adapter, dest.ID, r.serverKey)
}

// GetDedupStats opens (read-only) the dedup repo at dest and returns a
// snapshot of its in-memory Stats. Used by the
// GET /api/v1/storage/{id}/dedup-stats handler. Returns an error if dest
// is not dedup-enabled. When the repo has not been initialised yet (no
// backup has run, so _vault/repo.json does not exist) returns a zero-stats
// snapshot with Enabled=true rather than an error — the Storage card calls
// this immediately after Add Storage and a 500 there renders as a broken
// card.
func (r *Runner) GetDedupStats(dest db.StorageDestination) (dedup.Stats, error) {
	if !dest.DedupEnabled {
		return dedup.Stats{}, fmt.Errorf("destination %q is not dedup-enabled", dest.Name)
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return dedup.Stats{}, fmt.Errorf("adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)
	if _, err := adapter.Stat("_vault/repo.json"); err != nil {
		// Repo not yet initialised — return a zero snapshot so the
		// Storage card renders "0 chunks · 0 packs" instead of a 500.
		// The handler adds the "enabled": true key.
		return dedup.Stats{}, nil
	}
	repo, err := dedup.OpenRepo(r.db, adapter, dest.ID, r.serverKey)
	if err != nil {
		return dedup.Stats{}, fmt.Errorf("open dedup repo: %w", err)
	}
	return repo.Stats(), nil
}

// GetDedupManifest opens the dedup repo at dest and returns the manifest
// for the given manifest ID. Used by the RestorePointContents API handler
// so the restore wizard's file picker can list files even for dedup
// restore points (which don't have a tar index sidecar — chunks live in
// /_vault/packs/ instead of per-item tar archives).
func (r *Runner) GetDedupManifest(dest db.StorageDestination, manifestID dedup.ID) (dedup.Manifest, error) {
	if !dest.DedupEnabled {
		return dedup.Manifest{}, fmt.Errorf("destination %q is not dedup-enabled", dest.Name)
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return dedup.Manifest{}, fmt.Errorf("adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)
	repo, err := dedup.OpenRepo(r.db, adapter, dest.ID, r.serverKey)
	if err != nil {
		return dedup.Manifest{}, fmt.Errorf("open dedup repo: %w", err)
	}
	return repo.GetManifest(manifestID)
}

// ResolveItemManifestID is the public counterpart of the private
// resolveManifestID helper. Used by API handlers that need to detect
// whether a (rp, item) pair is a dedup restore point and, if so, fetch its
// manifest ID without duplicating the metadata-parsing logic.
func ResolveItemManifestID(rp db.RestorePoint, itemName string) (dedup.ID, bool) {
	return resolveManifestID(rp, itemName)
}

// RunDedupGC runs a mark-and-sweep GC for the given destination. Intended
// to be invoked from an HTTP handler in a goroutine — broadcasts the
// result over the WebSocket hub as `dedup_gc_complete` and logs progress
// so operators can follow along in the daemon log.
func (r *Runner) RunDedupGC(dest db.StorageDestination, runID string) {
	if !dest.DedupEnabled {
		log.Printf("gc: refusing to run on non-dedup destination %d (%q)", dest.ID, dest.Name)
		r.Broadcast(map[string]any{
			"type":        "dedup_gc_complete",
			"gc_run_id":   runID,
			"destination": dest.ID,
			"status":      "failed",
			"error":       "destination is not dedup-enabled",
		})
		return
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		log.Printf("gc: adapter for %q: %v", dest.Name, err)
		r.Broadcast(map[string]any{
			"type":        "dedup_gc_complete",
			"gc_run_id":   runID,
			"destination": dest.ID,
			"status":      "failed",
			"error":       err.Error(),
		})
		return
	}
	defer storage.CloseAdapter(adapter)

	repo, err := dedup.OpenRepo(r.db, adapter, dest.ID, r.serverKey)
	if err != nil {
		log.Printf("gc: open repo for %q: %v", dest.Name, err)
		r.Broadcast(map[string]any{
			"type":        "dedup_gc_complete",
			"gc_run_id":   runID,
			"destination": dest.ID,
			"status":      "failed",
			"error":       err.Error(),
		})
		return
	}

	live, err := r.collectLiveManifestIDs(repo, dest.ID)
	if err != nil {
		log.Printf("gc: live ids for %q: %v", dest.Name, err)
		r.Broadcast(map[string]any{
			"type":        "dedup_gc_complete",
			"gc_run_id":   runID,
			"destination": dest.ID,
			"status":      "failed",
			"error":       err.Error(),
		})
		return
	}

	ratioStr, _ := r.db.GetSetting("dedup_compaction_min_dead_ratio", "0.5")
	ratio, perr := strconv.ParseFloat(ratioStr, 64)
	if perr != nil || ratio < 0 || ratio > 1 {
		log.Printf("gc: invalid dedup_compaction_min_dead_ratio %q, falling back to 0.5 (err: %v)", ratioStr, perr)
		ratio = 0.5
	}

	result, gcErr := dedup.RunGC(repo, live, dedup.GCOptions{CompactMinDeadRatio: ratio})
	status := "completed"
	var errMsg string
	if gcErr != nil {
		status = "failed"
		errMsg = gcErr.Error()
		log.Printf("gc: %q: %v", dest.Name, gcErr)
	}
	log.Printf("gc: dest=%q run=%s freed_packs=%d freed_bytes=%d compacted_packs=%d reclaimed_bytes=%d rewritable=%d errors=%d",
		dest.Name, runID, result.FreedPacks, result.FreedBytes,
		result.CompactedPacks, result.ReclaimedBytes,
		result.RewritableBytes, len(result.Errors))

	msg := map[string]any{
		"type":             "dedup_gc_complete",
		"gc_run_id":        runID,
		"destination":      dest.ID,
		"status":           status,
		"freed_packs":      result.FreedPacks,
		"freed_bytes":      result.FreedBytes,
		"compacted_packs":  result.CompactedPacks,
		"reclaimed_bytes":  result.ReclaimedBytes,
		"rewritable_bytes": result.RewritableBytes,
		"errors":           result.Errors,
	}
	if errMsg != "" {
		msg["error"] = errMsg
	}
	r.Broadcast(msg)
}

// collectLiveManifestIDs returns every dedup manifest ID that GC must treat as
// reachable for the given destination. It reads each restore point's top-level
// manifest IDs — both restore_points.manifest_id (the single-item shortcut) and
// the per-item hex IDs under restore_points.metadata.item_manifests (multi-item
// jobs) — and then expands that set through engine.WalkManifestClosure so that
// container-volume *sub-manifests* are included too.
//
// The expansion is essential: a container manifest references file data only
// via __vol__ sub-manifests. GC's mark phase only marks the direct chunks of
// each manifest in this list, so without the sub-manifest IDs here, every
// data-only pack of a multi-pack container/plugin backup would be swept (silent
// data loss). A manifest that can't be read (e.g. a partial/corrupt restore
// point) is skipped rather than aborting GC for the whole destination.
func (r *Runner) collectLiveManifestIDs(repo *dedup.Repo, destID int64) ([]dedup.ID, error) {
	rows, err := r.db.Query(`
        SELECT manifest_id, metadata
          FROM restore_points
         WHERE job_id IN (SELECT id FROM jobs WHERE storage_dest_id = ?)`, destID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tops := make([]dedup.ID, 0, 16)
	seenTop := make(map[dedup.ID]struct{})
	addTop := func(id dedup.ID) {
		if _, ok := seenTop[id]; ok {
			return
		}
		seenTop[id] = struct{}{}
		tops = append(tops, id)
	}
	for rows.Next() {
		var (
			mID      []byte
			metadata sql.NullString
		)
		if err := rows.Scan(&mID, &metadata); err != nil {
			return nil, err
		}
		if len(mID) == 32 {
			var id dedup.ID
			copy(id[:], mID)
			addTop(id)
		}
		if metadata.Valid && metadata.String != "" {
			var meta map[string]any
			if err := json.Unmarshal([]byte(metadata.String), &meta); err == nil {
				if im, ok := meta["item_manifests"].(map[string]any); ok {
					for _, v := range im {
						hexStr, ok := v.(string)
						if !ok || hexStr == "" {
							continue
						}
						raw, derr := hex.DecodeString(hexStr)
						if derr != nil || len(raw) != 32 {
							continue
						}
						var id dedup.ID
						copy(id[:], raw)
						addTop(id)
					}
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Expand top-level manifests through their container-volume sub-manifests
	// so GC marks the nested data chunks too.
	seen := make(map[dedup.ID]struct{})
	out := make([]dedup.ID, 0, len(tops))
	for _, top := range tops {
		manifests, _, werr := engine.WalkManifestClosure(repo, []dedup.ID{top})
		if werr != nil {
			log.Printf("gc: skipping unreadable manifest %x in reachability walk: %v", top[:8], werr)
			// Still treat the top itself as reachable so we never delete a
			// pack solely because one restore point's manifest is unreadable.
			if _, ok := seen[top]; !ok {
				seen[top] = struct{}{}
				out = append(out, top)
			}
			continue
		}
		for _, id := range manifests {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out, nil
}

// backupItem executes a single item backup using the appropriate engine handler,
// writing output to a local temp dir and then to the storage adapter.
// If verify is true, it reads each file back and validates SHA-256 checksums.
// If passphrase is non-empty, each file is encrypted with age before uploading.
//
// When dest.DedupEnabled is true and the resolved handler implements
// engine.ChunkedHandler, the call is routed to backupItemChunked instead of
// the classic tar pipeline. Handlers that don't support chunking (vm, zfs)
// transparently fall through to the classic path even on dedup destinations.
func (r *Runner) backupItem(ctx context.Context, item engine.BackupItem, dest db.StorageDestination, storagePath string, verify bool, passphrase string, compression string, concurrency int) (*engine.BackupResult, map[string]string, error) {
	if dest.DedupEnabled {
		handler, err := newHandler(item.Type)
		if err != nil {
			return nil, nil, err
		}
		if chunked, ok := handler.(engine.ChunkedHandler); ok {
			return r.backupItemChunked(ctx, item, dest, chunked)
		}
		// Fall through to classic tar for non-chunked handlers (VM, ZFS).
	}

	tmpDir, result, cleanup, err := r.stageItemLocally(ctx, item, dest)
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()

	checksums, err := r.uploadStagedFilesN(ctx, tmpDir, dest, storagePath, verify, passphrase, compression, item.Type, item.Name, concurrency)
	if err != nil {
		return nil, nil, err
	}
	return result, checksums, nil
}

// backupItemChunked runs a single item backup via the dedup path. It opens
// (or initialises on first use) the dedup repo at dest, invokes the
// handler's BackupChunked, flushes any pending pack, and returns a
// BackupResult whose Meta carries the manifest ID for the runner to
// persist on the resulting restore_points row.
func (r *Runner) backupItemChunked(ctx context.Context, item engine.BackupItem, dest db.StorageDestination, handler engine.ChunkedHandler) (*engine.BackupResult, map[string]string, error) {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)

	// Pack uploads happen inside the handler's chunk walk; heartbeat the
	// stall watchdog on every byte written so a slow remote can't trigger a
	// false stall-cancel between per-file progress callbacks (issue #110).
	heartbeat := &heartbeatAdapter{Adapter: adapter, beat: func(int64) {
		r.lastProgressMu.Lock()
		r.lastProgress = time.Now()
		r.lastProgressMu.Unlock()
	}}

	repo, err := r.openDedupRepo(heartbeat, dest)
	if err != nil {
		return nil, nil, fmt.Errorf("open dedup repo: %w", err)
	}

	progress := func(name string, pct int, msg string) {
		r.lastProgressMu.Lock()
		r.lastProgress = time.Now()
		r.lastProgressMu.Unlock()

		r.updateCurrentItemProgress(item.Type, pct, msg)
		r.broadcast(map[string]any{
			"type":      "backup_progress",
			"item":      name,
			"item_type": item.Type,
			"percent":   pct,
			"message":   msg,
		})
	}

	manifestID, err := handler.BackupChunked(ctx, item, repo, progress)
	if err != nil {
		return nil, nil, fmt.Errorf("backup chunked: %w", err)
	}
	if err := repo.Flush(); err != nil {
		return nil, nil, fmt.Errorf("repo flush: %w", err)
	}

	stats := repo.Stats()
	itemLogical := repo.SessionLogicalBytes()
	log.Printf("runner: dedup item=%q manifest=%x chunks_total=%d packs_total=%d session_logical=%dB physical=%dB",
		item.Name, manifestID[:8], stats.TotalChunks, stats.TotalPacks, itemLogical, stats.PhysicalBytes)

	midCopy := append([]byte(nil), manifestID[:]...)
	// The existing per-item byte accounting in RunJob sums result.Files[].Size
	// to populate restore_points.size_bytes. For dedup runs we don't write
	// distinct files, so report a single synthetic entry whose size is the
	// session's plaintext-bytes total (every Put through this Repo instance,
	// dedupe-hit or not). That keeps the Storage card's dedup_ratio
	// directionally correct across snapshots: each restore_point row
	// contributes its full logical size to the numerator while the chunk
	// store stays a single shared denominator.
	result := &engine.BackupResult{
		ItemName: item.Name,
		Success:  true,
		Files: []engine.BackupFile{{
			Name: "__manifest:" + item.Name,
			Size: itemLogical,
		}},
		Meta: map[string]any{
			"manifest_id":    midCopy,
			"dedup_logical":  itemLogical,
			"dedup_physical": stats.PhysicalBytes,
			"dedup_chunks":   stats.TotalChunks,
			"dedup_packs":    stats.TotalPacks,
		},
	}
	return result, nil, nil
}

// stageItemLocally creates a temp directory and runs the appropriate engine
// handler to stage the item's archive(s) on local disk. It returns the staging
// dir, the backup result, and a cleanup func the caller must invoke when the
// staged files are no longer needed (callers may defer it immediately for the
// non-deferred path, or hold it across the upload phase for deferred mode).
func (r *Runner) stageItemLocally(ctx context.Context, item engine.BackupItem, dest db.StorageDestination) (string, *engine.BackupResult, func(), error) {
	stageOverride, _ := r.db.GetSetting("staging_dir_override", "")
	tmpDir, cleanup, err := tempdir.CreateBackupDir(tempdir.StorageConfig{Type: dest.Type, Config: dest.Config}, stageOverride)
	if err != nil {
		return "", nil, func() {}, fmt.Errorf("creating temp dir: %w", err)
	}

	var handler engine.Handler
	switch item.Type {
	case "container":
		handler, err = engine.NewContainerHandler()
	case "vm":
		handler, err = engine.NewVMHandler()
	case "folder":
		handler, err = engine.NewFolderHandler()
	case "plugin":
		handler, err = engine.NewPluginHandler()
	case "zfs":
		handler, err = engine.NewZFSHandler()
	default:
		cleanup()
		return "", nil, func() {}, fmt.Errorf("unknown item type: %s", item.Type)
	}
	if err != nil {
		cleanup()
		return "", nil, func() {}, fmt.Errorf("creating %s handler: %w", item.Type, err)
	}

	progress := func(name string, pct int, msg string) {
		r.lastProgressMu.Lock()
		r.lastProgress = time.Now()
		r.lastProgressMu.Unlock()

		r.updateCurrentItemProgress(item.Type, pct, msg)
		r.broadcast(map[string]any{
			"type":      "backup_progress",
			"item":      name,
			"item_type": item.Type,
			"percent":   pct,
			"message":   msg,
		})
	}

	result, err := handler.Backup(ctx, item, tmpDir, progress)
	if err != nil {
		log.Printf("runner: backup %s %q failed: %v", item.Type, item.Name, err)
		cleanup()
		return "", nil, func() {}, fmt.Errorf("backup %s: %w", item.Name, err)
	}
	return tmpDir, result, cleanup, nil
}

// uploadStagedFilesN streams every regular file in tmpDir through the
// encryption pipeline to the storage adapter, computes SHA-256 checksums during
// upload, and (optionally) verifies by re-reading. Files are uploaded through a
// bounded worker pool of size concurrency (clamped to >=1).
//
// Transient remote-storage failures are retried inside the storage layer via
// adapter.WriteFrom: the per-file reopen closure re-opens the staged file and
// re-applies encryption each attempt, so the retry layer can replay the upload
// from a fresh stream. The runner no longer runs its own per-file backoff loop.
//
// Compression for archive-producing item types (container, folder, plugin)
// is applied by the engine when it produces the archive, so the runner does
// not wrap those uploads — historically the engine always emitted .tar.gz and
// the runner added a second transport-layer compression on top, which yielded
// double-compressed files and ignored the user's "None" selection entirely.
// VM backups are the exception: the engine stages raw artifacts (disk images,
// domain.xml, vm_meta.json, NVRAM) with no codec of their own, so the job's
// compression is applied here as a transport wrap. The restore path peels it
// off content-based via decompressStoredReader, the same mechanism used for
// legacy double-wrapped backups.
func (r *Runner) uploadStagedFilesN(ctx context.Context, tmpDir string, dest db.StorageDestination, storagePath string, verify bool, passphrase string, compression string, itemType string, itemName string, concurrency int) (map[string]string, error) {
	transportCompression := "none"
	if itemType == "vm" {
		transportCompression = compression
	}

	progress := func(pct int, msg string) {
		r.lastProgressMu.Lock()
		r.lastProgress = time.Now()
		r.lastProgressMu.Unlock()

		r.updateCurrentItemProgress(itemType, pct, msg)
		r.broadcast(map[string]any{
			"type":      "backup_progress",
			"item":      itemName,
			"item_type": itemType,
			"percent":   pct,
			"message":   msg,
		})
	}

	verbose, vErr := r.db.GetSettingBool("storage_verbose_logging", false)
	if vErr != nil {
		log.Printf("runner: reading storage_verbose_logging setting: %v", vErr)
	}
	adapter, err := storage.NewAdapterWithOptions(dest.Type, dest.Config, storage.Options{
		VerboseLogging: verbose,
		DestLabel:      dest.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("creating storage adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)
	// If the job context is cancelled, close the adapter so any storage
	// operation blocked acquiring a pooled connection unblocks promptly.
	stopOnCancel := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stopOnCancel()

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("reading backup dir: %w", err)
	}

	if concurrency < 1 {
		concurrency = 1
	}
	var (
		mu        sync.Mutex
		checksums = make(map[string]string)
		sem       = make(chan struct{}, concurrency)
		wg        sync.WaitGroup
		firstErr  error
		errOnce   sync.Once
	)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(entryName string) {
			defer wg.Done()
			defer func() { <-sem }()
			storageName, checksum, upErr := r.uploadOneStaged(ctx, adapter, tmpDir, entryName, storagePath, passphrase, transportCompression, itemType, itemName, progress)
			if storageName != "" {
				mu.Lock()
				checksums[storageName] = checksum // checksum is "" on error; key still enables cleanup
				mu.Unlock()
			}
			if upErr != nil {
				errOnce.Do(func() { firstErr = upErr })
				return
			}
		}(entry.Name())
	}
	wg.Wait()
	if firstErr != nil {
		r.cleanupPartialUploads(adapter, storagePath, checksums)
		return nil, firstErr
	}
	if ctx.Err() != nil {
		r.cleanupPartialUploads(adapter, storagePath, checksums)
		return nil, fmt.Errorf("upload cancelled: %w", ctx.Err())
	}

	// Verify: read files back from storage and re-compute SHA-256.
	if verify {
		for fileName, expectedHash := range checksums {
			destPath := filepath.Join(storagePath, fileName)
			reader, err := adapter.Read(destPath)
			if err != nil {
				r.cleanupPartialUploads(adapter, storagePath, checksums)
				return nil, fmt.Errorf("verification read %s: %w", fileName, err)
			}
			verifyHasher := sha256.New()
			// Heartbeat the stall watchdog while reading the file back — a
			// large verify read is just as long-running as the upload and
			// would otherwise re-freeze the progress timer (issue #110).
			var verifyBroadcast time.Time
			verifyReader := &countingReader{
				reader: reader,
				onRead: func(int64) {
					r.lastProgressMu.Lock()
					r.lastProgress = time.Now()
					r.lastProgressMu.Unlock()
					if time.Since(verifyBroadcast) >= time.Second {
						verifyBroadcast = time.Now()
						progress(0, fmt.Sprintf("Verifying %s", fileName))
					}
				},
			}
			if _, err := io.Copy(verifyHasher, verifyReader); err != nil {
				_ = reader.Close()
				r.cleanupPartialUploads(adapter, storagePath, checksums)
				return nil, fmt.Errorf("verification hash %s: %w", fileName, err)
			}
			_ = reader.Close()
			actualHash := hex.EncodeToString(verifyHasher.Sum(nil))
			if actualHash != expectedHash {
				r.cleanupPartialUploads(adapter, storagePath, checksums)
				return nil, fmt.Errorf("verification failed for %s: expected %s, got %s", fileName, expectedHash, actualHash)
			}
		}
	}

	return checksums, nil
}

// uploadOneStaged uploads one staged file via the adapter's retry-aware
// WriteFrom. The reopen closure yields a fresh (re-encrypted) stream each
// attempt; the sha256 hasher is reset at the start of every attempt and tees
// the bytes actually sent, so after a successful WriteFrom it holds the digest
// of the bytes that were stored — correct even if the storage layer retried.
// It returns (storageName, checksumHex, err). All per-attempt state (hasher,
// uploaded counter, broadcast timer) is local so concurrent calls never share
// mutable state.
func (r *Runner) uploadOneStaged(ctx context.Context, adapter storage.Adapter, tmpDir, entryName, storagePath, passphrase, transportCompression, itemType, itemName string, progress func(int, string)) (string, string, error) {
	_ = itemType
	_ = itemName

	filePath := filepath.Join(tmpDir, entryName)
	// Stat for the source size so we can report an upload percentage. A failure
	// here is non-fatal: we fall back to size 0, which disables the percentage
	// but still heartbeats the stall watchdog.
	var fileSize int64
	if fi, err := os.Stat(filePath); err == nil {
		fileSize = fi.Size()
	}

	// Compression is applied before encryption (encrypted bytes don't
	// compress) and both leave their mark on the stored name so the restore
	// path can peel the layers back off in order.
	compressSuffix := transportCompressionSuffix(transportCompression)
	storageName := entryName + compressSuffix
	if passphrase != "" {
		storageName += ".age"
	}
	destPath := filepath.Join(storagePath, storageName)

	hasher := sha256.New()
	var uploaded int64
	var lastBroadcast time.Time

	// reopen yields a fresh source stream for each WriteFrom attempt. It resets
	// the hasher and byte counter first so a replayed attempt re-hashes from the
	// start and the digest matches exactly the bytes of the successful attempt.
	reopen := func() (io.ReadCloser, error) {
		hasher.Reset()
		uploaded = 0
		f, err := os.Open(filePath) // #nosec G304 — tmpDir is a vault-controlled temp directory; entryName from os.ReadDir
		if err != nil {
			return nil, err
		}
		var src io.Reader = f
		closers := []io.Closer{f}
		if compressSuffix != "" {
			comp, compErr := transportCompressReader(transportCompression, f)
			if compErr != nil {
				_ = f.Close()
				return nil, fmt.Errorf("compressing %s: %w", entryName, compErr)
			}
			src = comp
			closers = append(closers, comp)
		}
		if passphrase != "" {
			enc, encErr := crypto.EncryptReader(passphrase, src)
			if encErr != nil {
				for i := len(closers) - 1; i >= 0; i-- {
					_ = closers[i].Close()
				}
				return nil, fmt.Errorf("encrypting %s: %w", entryName, encErr)
			}
			src = enc
			closers = append(closers, enc)
		}
		// Tee the bytes-to-be-sent into the hasher, then count them so the stall
		// watchdog (issue #110) sees bytes moving during the blocking write and
		// the user-facing broadcast is rate-limited to ~1/second.
		tee := io.TeeReader(src, hasher)
		counted := &countingReader{
			reader: tee,
			onRead: func(n int64) {
				uploaded += n
				r.lastProgressMu.Lock()
				r.lastProgress = time.Now()
				r.lastProgressMu.Unlock()
				if time.Since(lastBroadcast) >= time.Second {
					lastBroadcast = time.Now()
					pct := 0
					if fileSize > 0 {
						pct = int(uploaded * 100 / fileSize)
						if pct > 100 {
							pct = 100
						}
					}
					progress(pct, fmt.Sprintf("Uploading %s", entryName))
				}
			},
		}
		return &multiCloseReader{Reader: counted, closers: closers}, nil
	}

	if err := adapter.WriteFrom(destPath, reopen); err != nil {
		if ctx.Err() != nil {
			return storageName, "", fmt.Errorf("upload cancelled: %w", ctx.Err())
		}
		return storageName, "", fmt.Errorf("writing %s to storage: %w", storageName, err)
	}
	return storageName, hex.EncodeToString(hasher.Sum(nil)), nil
}

// cleanupPartialUploads best-effort deletes the files named in checksums under
// storagePath after an overall upload/verify failure. Leaving partials behind
// wastes remote storage quota and makes the next run look "half done". Delete
// errors are logged but not surfaced — the original failure is what the caller
// cares about (see issue #83 follow-up).
func (r *Runner) cleanupPartialUploads(adapter storage.Adapter, storagePath string, checksums map[string]string) {
	if len(checksums) == 0 {
		return
	}
	log.Printf("runner: cleaning up %d partial upload(s) under %s after failure", len(checksums), storagePath)
	for name := range checksums {
		p := filepath.Join(storagePath, name)
		if delErr := adapter.Delete(p); delErr != nil {
			log.Printf("runner: cleanup: failed to delete orphaned %s: %v", p, delErr)
		}
	}
}

// multiCloseReader wraps a reader together with the closers that must be
// released when the upload stream is done, closing them in reverse order so
// the encryption pipe is unblocked before the underlying file.
type multiCloseReader struct {
	io.Reader
	closers []io.Closer
}

func (m *multiCloseReader) Close() error {
	var err error
	for i := len(m.closers) - 1; i >= 0; i-- {
		if e := m.closers[i].Close(); e != nil && err == nil {
			err = e
		}
	}
	return err
}

// RestoreTarget describes a single item to restore. When FilePaths is non-nil
// the restore extracts only those tar entries from the item's archive(s).
// Paths use the same form as they appear in the tar index sidecar
// (forward-slash separated, no leading slash). An empty/nil FilePaths means
// "restore everything in this item" (legacy behaviour).
type RestoreTarget struct {
	Name      string
	Type      string
	FilePaths []string
}

// startStallWatchdog launches a goroutine that cancels ctx when no progress
// (r.lastProgress) has advanced for cancelAfter, warning at warn. It returns
// when ctx is done. Shared by backup and restore runs.
func (r *Runner) startStallWatchdog(ctx context.Context, cancel context.CancelFunc, jobID int64, tick, warn, cancelAfter time.Duration) {
	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.lastProgressMu.Lock()
				idle := time.Since(r.lastProgress)
				r.lastProgressMu.Unlock()
				if idle >= cancelAfter {
					log.Printf("runner: job %d stalled for %v — cancelling", jobID, idle.Truncate(time.Minute))
					cancel()
					return
				}
				if idle >= warn {
					log.Printf("runner: WARNING job %d no progress for %v", jobID, idle.Truncate(time.Minute))
				}
			}
		}
	}()
}

// RunRestore executes a tracked restore operation. It creates a job_run
// record with run_type="restore", restores each target item, updates
// progress via WebSocket, and finalises the run record.
func (r *Runner) RunRestore(restorePoint db.RestorePoint, targets []RestoreTarget, destination, passphrase string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	jobName := fmt.Sprintf("Job #%d", restorePoint.JobID)
	if job, err := r.db.GetJob(restorePoint.JobID); err == nil {
		jobName = job.Name
	}

	run := db.JobRun{
		JobID:      restorePoint.JobID,
		Status:     "running",
		BackupType: restorePoint.BackupType,
		RunType:    "restore",
		ItemsTotal: len(targets),
	}
	runID, err := r.db.CreateJobRun(run)
	if err != nil {
		log.Printf("runner: failed to create restore run: %v", err)
		return
	}
	run.ID = runID

	// Create a cancellable context for the restore run. Like the backup path,
	// there is no wall-clock deadline — the stall watchdog is the sole
	// automatic canceller. CancelJob() triggers the stored cancel func on demand.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.cancelMu.Lock()
	r.cancelFn = cancel
	r.cancellingJobID = restorePoint.JobID
	r.cancelMu.Unlock()
	defer func() {
		r.cancelMu.Lock()
		r.cancelFn = nil
		r.cancellingJobID = 0
		r.cancelMu.Unlock()
	}()

	r.lastProgressMu.Lock()
	r.lastProgress = time.Now()
	r.lastProgressMu.Unlock()
	r.startStallWatchdog(ctx, cancel, restorePoint.JobID, 5*time.Minute, stallWarnInterval, stallCancelTimeout)

	targetNames := make([]string, 0, len(targets))
	for _, t := range targets {
		targetNames = append(targetNames, t.Name)
	}

	r.broadcast(map[string]any{
		"type":        "job_run_started",
		"job_id":      restorePoint.JobID,
		"run_id":      runID,
		"job_name":    jobName,
		"run_type":    "restore",
		"items_total": len(targets),
	})

	restoreStart := time.Now()

	r.setRunStatus(&RunStatus{
		Active:     true,
		JobID:      restorePoint.JobID,
		RunID:      runID,
		JobName:    jobName,
		RunType:    "restore",
		ItemsTotal: len(targets),
		StartedAt:  restoreStart.Format(time.RFC3339),
	})

	r.logActivity("info", "restore",
		fmt.Sprintf("Restore started: %d item(s) from restore point %d", len(targets), restorePoint.ID),
		structuredDetails(map[string]any{
			"job_id":           restorePoint.JobID,
			"run_id":           runID,
			"restore_point_id": restorePoint.ID,
			"items":            len(targets),
			"items_list":       targetNames,
		}))

	// Recover from panics so the run is marked failed.
	defer func() {
		r.setRunStatus(nil)
		if rec := recover(); rec != nil {
			log.Printf("runner: PANIC during restore run %d: %v", runID, rec)
			run.Status = "failed"
			run.Log = fmt.Sprintf("Internal error (panic): %v", rec)
			_ = r.db.UpdateJobRun(run)
			r.broadcast(map[string]any{
				"type":     "job_run_completed",
				"job_id":   restorePoint.JobID,
				"run_id":   runID,
				"run_type": "restore",
				"status":   "failed",
			})
		}
	}()

	var (
		itemsDone   int
		itemsFailed int
		itemResults []map[string]any
	)

	for _, t := range targets {
		reporter := restoreProgressReporter{
			JobID:       restorePoint.JobID,
			RunID:       runID,
			ItemName:    t.Name,
			ItemType:    t.Type,
			ItemsDone:   itemsDone,
			ItemsFailed: itemsFailed,
			ItemsTotal:  len(targets),
		}

		r.broadcast(map[string]any{
			"type":         "item_restore_start",
			"job_id":       restorePoint.JobID,
			"run_id":       runID,
			"item_name":    t.Name,
			"item_type":    t.Type,
			"items_done":   itemsDone,
			"items_failed": itemsFailed,
			"items_total":  len(targets),
		})
		r.updateRunProgress(itemsDone, itemsFailed, t.Name)
		r.reportRestoreProgress(reporter, 0, "Starting...")

		start := time.Now()
		restoreErr := r.restoreItemWithReporter(ctx, restorePoint, t.Name, t.Type, destination, passphrase, t.FilePaths, reporter)
		elapsed := time.Since(start)

		result := map[string]any{
			"name":     t.Name,
			"type":     t.Type,
			"duration": elapsed.String(),
		}

		if restoreErr != nil {
			itemsFailed++
			result["status"] = "failed"
			result["error"] = restoreErr.Error()
			r.broadcast(map[string]any{
				"type":         "item_restore_failed",
				"job_id":       restorePoint.JobID,
				"run_id":       runID,
				"item_name":    t.Name,
				"item_type":    t.Type,
				"error":        restoreErr.Error(),
				"items_done":   itemsDone,
				"items_failed": itemsFailed,
				"items_total":  len(targets),
			})
		} else {
			itemsDone++
			result["status"] = "ok"
			r.broadcast(map[string]any{
				"type":         "item_restore_done",
				"job_id":       restorePoint.JobID,
				"run_id":       runID,
				"item_name":    t.Name,
				"item_type":    t.Type,
				"items_done":   itemsDone,
				"items_failed": itemsFailed,
				"items_total":  len(targets),
			})
		}

		itemResults = append(itemResults, result)
		_ = r.db.UpdateJobRunProgress(runID, itemsDone, itemsFailed, 0)
		r.updateRunProgress(itemsDone, itemsFailed, "")
	}

	// Finalise the run.
	status := "completed"
	if itemsFailed > 0 && itemsDone > 0 {
		status = "partial"
	} else if itemsFailed > 0 {
		status = "failed"
	}

	logJSON, _ := json.Marshal(itemResults)
	run.Status = status
	run.Log = string(logJSON)
	run.ItemsDone = itemsDone
	run.ItemsFailed = itemsFailed
	run.SizeBytes = restorePoint.SizeBytes
	_ = r.db.UpdateJobRun(run)

	r.broadcast(map[string]any{
		"type":         "job_run_completed",
		"job_id":       restorePoint.JobID,
		"run_id":       runID,
		"run_type":     "restore",
		"status":       status,
		"items_done":   itemsDone,
		"items_failed": itemsFailed,
		"items_total":  len(targets),
		"size_bytes":   restorePoint.SizeBytes,
	})

	level := "info"
	msg := fmt.Sprintf("Restore completed: %s (%d/%d items)", status, itemsDone, len(targets))
	if itemsFailed > 0 {
		level = "warning"
	}
	if status == "failed" {
		level = "error"
		msg = fmt.Sprintf("Restore failed: all %d items failed", len(targets))
	}
	r.logActivity(level, "restore", msg,
		structuredDetails(map[string]any{
			"job_id":           restorePoint.JobID,
			"job_name":         jobName,
			"run_id":           runID,
			"items_done":       itemsDone,
			"items_failed":     itemsFailed,
			"items_total":      len(targets),
			"size_bytes":       restorePoint.SizeBytes,
			"duration_seconds": int(time.Since(restoreStart).Seconds()),
		}))
}

// RestoreItem restores a single item from a restore point.
// If destination is non-empty, it overrides the original restore path.
// If passphrase is non-empty, .age files are decrypted before restoring.
// For incremental/differential restore points, the full chain is restored
// in order (base full → incremental/differential overlays).
func (r *Runner) RestoreItem(restorePoint db.RestorePoint, itemName, itemType, destination, passphrase string) error {
	// Deliberately context.Background(): this is the un-tracked scripted/MCP
	// entry point with no registered cancel func or stall watchdog. The tracked
	// RunRestore path threads its own cancellable ctx through the chain.
	return r.restoreItemWithReporter(context.Background(), restorePoint, itemName, itemType, destination, passphrase, nil, restoreProgressReporter{})
}

func (r *Runner) restoreItemWithReporter(ctx context.Context, restorePoint db.RestorePoint, itemName, itemType, destination, passphrase string, filePaths []string, reporter restoreProgressReporter) error {
	// For incremental/differential, walk the chain and restore in order.
	if restorePoint.BackupType == "incremental" || restorePoint.BackupType == "differential" {
		chain, err := r.buildRestoreChain(restorePoint)
		if err != nil {
			return fmt.Errorf("building restore chain: %w", err)
		}
		if usesMergedRestoreChain(itemType) {
			return r.restoreMergedChain(ctx, chain, itemName, itemType, destination, passphrase, filePaths, reporter)
		}
		for i, rp := range chain {
			log.Printf("runner: restoring chain step %d/%d (type=%s, id=%d)",
				i+1, len(chain), rp.BackupType, rp.ID)
			if err := r.restoreSinglePoint(ctx, rp, itemName, itemType, destination, passphrase, filePaths, reporter); err != nil {
				return fmt.Errorf("restoring chain step %d (id=%d): %w", i+1, rp.ID, err)
			}
		}
		return nil
	}
	return r.restoreSinglePoint(ctx, restorePoint, itemName, itemType, destination, passphrase, filePaths, reporter)
}

func usesMergedRestoreChain(itemType string) bool {
	switch itemType {
	case "container", "vm":
		return true
	default:
		return false
	}
}

func (r *Runner) restoreMergedChain(ctx context.Context, chain []db.RestorePoint, itemName, itemType, destination, passphrase string, filePaths []string, reporter restoreProgressReporter) error {
	stageOverride, _ := r.db.GetSetting("staging_dir_override", "")
	tmpDir, cleanup, err := tempdir.CreateRestoreDir(tempdir.StorageConfig{}, stageOverride)
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer cleanup()

	// VM qcow2 chains must be assembled per-step then flattened with
	// qemu-img so each chain step keeps its own dirty-block deltas. For
	// non-VM items we keep the simple flat staging where each chain step
	// can safely overlay files in the same directory (folder rsync,
	// container layered tar, etc.).
	if itemType == "vm" && len(chain) > 1 {
		stepDirs := make([]string, 0, len(chain))
		for i, rp := range chain {
			log.Printf("runner: staging VM chain step %d/%d (type=%s, id=%d)", i+1, len(chain), rp.BackupType, rp.ID)
			stepDir := filepath.Join(tmpDir, fmt.Sprintf("step_%d", i))
			if err := os.MkdirAll(stepDir, 0o755); err != nil {
				return fmt.Errorf("creating chain step dir: %w", err)
			}
			phaseStart := (i * 30) / len(chain)
			phaseEnd := ((i + 1) * 30) / len(chain)
			if err := r.stageRestorePointItem(ctx, rp, itemName, stepDir, passphrase, phaseStart, phaseEnd, reporter); err != nil {
				return fmt.Errorf("staging VM chain step %d (id=%d): %w", i+1, rp.ID, err)
			}
			stepDirs = append(stepDirs, stepDir)
		}

		r.reportRestoreProgress(reporter, 30, "Flattening VM chain")
		flattenedDir := filepath.Join(tmpDir, "flat")
		if err := flattenVMChain(ctx, stepDirs, flattenedDir); err != nil {
			return fmt.Errorf("flattening VM chain: %w", err)
		}
		return r.restoreStagedItem(ctx, chain[len(chain)-1].JobID, itemName, itemType, destination, flattenedDir, filePaths, reporter, 40, 100)
	}

	for i, rp := range chain {
		log.Printf("runner: staging chain step %d/%d (type=%s, id=%d)", i+1, len(chain), rp.BackupType, rp.ID)
		phaseStart := (i * 40) / len(chain)
		phaseEnd := ((i + 1) * 40) / len(chain)
		if err := r.stageRestorePointItem(ctx, rp, itemName, tmpDir, passphrase, phaseStart, phaseEnd, reporter); err != nil {
			return fmt.Errorf("staging chain step %d (id=%d): %w", i+1, rp.ID, err)
		}
	}

	return r.restoreStagedItem(ctx, chain[len(chain)-1].JobID, itemName, itemType, destination, tmpDir, filePaths, reporter, 40, 100)
}

// restoreSinglePoint restores a single restore point (without chain logic).
// For dedup restore points (manifest_id set, or item_manifests in metadata),
// the chunked restore path is taken instead of the classic stage + restore.
func (r *Runner) restoreSinglePoint(ctx context.Context, restorePoint db.RestorePoint, itemName, itemType, destination, passphrase string, filePaths []string, reporter restoreProgressReporter) error {
	if manifestID, ok := resolveManifestID(restorePoint, itemName); ok {
		return r.restoreSinglePointChunked(ctx, restorePoint, manifestID, itemName, itemType, destination, reporter)
	}

	stageOverride, _ := r.db.GetSetting("staging_dir_override", "")
	tmpDir, cleanup, err := tempdir.CreateRestoreDir(tempdir.StorageConfig{}, stageOverride)
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer cleanup()

	if err := r.stageRestorePointItem(ctx, restorePoint, itemName, tmpDir, passphrase, 0, 40, reporter); err != nil {
		return err
	}

	return r.restoreStagedItem(ctx, restorePoint.JobID, itemName, itemType, destination, tmpDir, filePaths, reporter, 40, 100)
}

// resolveManifestID returns the dedup manifest ID for itemName from a
// restore point. It first consults metadata["item_manifests"][itemName] (a
// hex string written by the backup path for multi-item dedup jobs), then
// falls back to restorePoint.ManifestID for single-item dedup jobs. The
// ok=false case means this is a classic (non-dedup) restore point.
func resolveManifestID(rp db.RestorePoint, itemName string) (dedup.ID, bool) {
	if rp.Metadata != "" {
		var meta map[string]any
		if err := json.Unmarshal([]byte(rp.Metadata), &meta); err == nil {
			if itemMap, ok := meta["item_manifests"].(map[string]any); ok {
				if hexID, ok := itemMap[itemName].(string); ok && hexID != "" {
					if decoded, err := hex.DecodeString(hexID); err == nil && len(decoded) == 32 {
						var id dedup.ID
						copy(id[:], decoded)
						return id, true
					}
				}
			}
		}
	}
	if len(rp.ManifestID) == 32 {
		var id dedup.ID
		copy(id[:], rp.ManifestID)
		return id, true
	}
	return dedup.ID{}, false
}

// restoreSinglePointChunked restores one item from a dedup restore point.
// It opens the dedup repo at the destination, resolves the handler (must
// implement engine.ChunkedHandler — otherwise it's a corrupted RP where a
// manifest_id was persisted for a handler that can't chunk), and invokes
// RestoreChunked. destPath is passed through to the handler so it can write
// directly to the target — no local staging required.
func (r *Runner) restoreSinglePointChunked(ctx context.Context, rp db.RestorePoint, manifestID dedup.ID, itemName, itemType, destination string, reporter restoreProgressReporter) error {
	job, err := r.db.GetJob(rp.JobID)
	if err != nil {
		return fmt.Errorf("getting job: %w", err)
	}
	dest, err := r.db.GetStorageDestination(job.StorageDestID)
	if err != nil {
		return fmt.Errorf("getting storage destination: %w", err)
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return fmt.Errorf("creating storage adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)

	repo, err := r.openDedupRepo(adapter, dest)
	if err != nil {
		return fmt.Errorf("open dedup repo: %w", err)
	}

	handler, err := newHandler(itemType)
	if err != nil {
		return err
	}
	chunked, ok := handler.(engine.ChunkedHandler)
	if !ok {
		return fmt.Errorf("restore: handler for %q does not support chunked restore but restore point %d has manifest_id (data corruption?)", itemType, rp.ID)
	}

	// Resolve restore destination: explicit override wins; otherwise pull
	// the original path from the job's item settings (folder items only;
	// container/plugin handlers determine their own destination internally).
	destPath := destination
	if destPath == "" && itemType == "folder" {
		if jobItems, itemsErr := r.db.GetJobItems(rp.JobID); itemsErr == nil {
			for _, ji := range jobItems {
				if ji.ItemName == itemName && ji.ItemType == "folder" {
					var s map[string]any
					if json.Unmarshal([]byte(ji.Settings), &s) == nil {
						if p, ok := s["path"].(string); ok {
							destPath = p
						}
					}
					break
				}
			}
		}
	}

	item := engine.BackupItem{
		Name: itemName,
		Type: itemType,
		Settings: map[string]any{
			"path": destPath,
		},
	}
	if destination != "" {
		item.Settings["restore_destination"] = destination
	}

	progress := func(name string, pct int, msg string) {
		r.lastProgressMu.Lock()
		r.lastProgress = time.Now()
		r.lastProgressMu.Unlock()
		reporter.ItemName = name
		r.reportRestoreProgress(reporter, pct, msg)
	}

	restoreErr := chunked.RestoreChunked(ctx, item, repo, manifestID, destPath, progress)
	r.sendRestoreNotification(itemName, itemType, restoreErr)
	return restoreErr
}

func (r *Runner) stageRestorePointItem(ctx context.Context, restorePoint db.RestorePoint, itemName, tmpDir, passphrase string, phaseStart, phaseEnd int, reporter restoreProgressReporter) error {
	job, err := r.db.GetJob(restorePoint.JobID)
	if err != nil {
		return fmt.Errorf("getting job: %w", err)
	}

	dest, err := r.db.GetStorageDestination(job.StorageDestID)
	if err != nil {
		return fmt.Errorf("getting storage destination: %w", err)
	}

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return fmt.Errorf("creating storage adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)
	stopOnCancel := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stopOnCancel()

	itemStoragePath := filepath.Join(restorePoint.StoragePath, itemName)

	// Parse checksums from restore point metadata for verification.
	expectedChecksums := r.parseItemChecksums(restorePoint.Metadata, itemName)

	files, err := adapter.List(itemStoragePath)
	if err != nil {
		return fmt.Errorf("listing restore files: %w", err)
	}

	restoreFiles := make([]storage.FileInfo, 0, len(files))
	var totalBytes int64
	for _, fi := range files {
		if fi.IsDir {
			continue
		}
		restoreFiles = append(restoreFiles, fi)
		if fi.Size > 0 {
			totalBytes += fi.Size
		}
	}

	if len(restoreFiles) == 0 {
		r.reportRestoreProgress(reporter, phaseEnd, "Restore data ready")
		return nil
	}

	r.reportRestoreProgress(reporter, phaseStart, "Preparing restore data")

	concurrency := job.EffectiveUploadConcurrency()
	if concurrency < 1 {
		concurrency = 1
	}

	var (
		dlMu     sync.Mutex
		dl       int64 // bytes downloaded across all files (for progress)
		sem      = make(chan struct{}, concurrency)
		wg       sync.WaitGroup
		firstErr error
		errOnce  sync.Once
	)
	heartbeat := func(n int64) {
		r.lastProgressMu.Lock()
		r.lastProgress = time.Now()
		r.lastProgressMu.Unlock()
		dlMu.Lock()
		dl += n
		cur := dl
		dlMu.Unlock()
		if totalBytes > 0 {
			r.reportRestoreProgress(reporter, scaleRestorePhaseProgress(phaseStart, phaseEnd, cur, totalBytes), "Downloading")
		}
	}

	for _, fi := range restoreFiles {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(fi storage.FileInfo) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := r.downloadRestoreFile(ctx, adapter, fi, tmpDir, passphrase, job.Compression, expectedChecksums, heartbeat, storage.RestorePartSize, concurrency); err != nil {
				errOnce.Do(func() { firstErr = err })
			}
		}(fi)
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.reportRestoreProgress(reporter, phaseEnd, "Restore data ready")

	return nil
}

// downloadRestoreFile fetches one stored object into tmpDir, choosing the
// parallel-range path for plain large objects and the sequential resumable path
// otherwise, then verifies the checksum when one is recorded.
//
// partSize and concurrency are passed explicitly so tests can exercise the
// parallel path without large allocations; production callers use
// storage.RestorePartSize and job.EffectiveUploadConcurrency().
func (r *Runner) downloadRestoreFile(ctx context.Context, adapter storage.Adapter, fi storage.FileInfo, tmpDir, passphrase, compression string, expected map[string]string, onBytes func(int64), partSize int64, concurrency int) error {
	storageName := filepath.Base(fi.Path)
	encrypted := strings.HasSuffix(storageName, ".age") && passphrase != ""

	if !encrypted && fi.Size >= 2*partSize && !r.objectIsCompressed(adapter, fi.Path) {
		return r.downloadParallelPlain(ctx, adapter, fi, tmpDir, expected, onBytes, partSize, concurrency)
	}
	return r.downloadSequentialResumable(ctx, adapter, fi, tmpDir, passphrase, compression, expected, onBytes)
}

// objectIsCompressed peeks the first 4 bytes to detect gzip/zstd transport
// compression — the same content-based test decompressStoredReader uses via
// looksCompressed. On any peek error it conservatively returns true (forcing
// the safe sequential path).
//
// It takes no context: the ReadRange call is not cancellable directly. To
// unblock a stuck peek on cancellation it relies on the caller having installed
// context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) }) — closing the
// adapter aborts the in-flight ReadRange. stageRestorePointItem (the only
// caller) installs that hook, so callers needing cancellation must do the same.
func (r *Runner) objectIsCompressed(adapter storage.Adapter, path string) bool {
	rc, err := adapter.ReadRange(path, 0, 4)
	if err != nil {
		return true // conservative: use sequential path
	}
	defer rc.Close()
	head := make([]byte, 4)
	n, _ := io.ReadFull(rc, head)
	return looksCompressed(head[:n])
}

// downloadParallelPlain assembles a plain (unencrypted, uncompressed) object
// via concurrent ranged GETs, then verifies the whole-file SHA-256 against the
// stored checksum when one is present.
func (r *Runner) downloadParallelPlain(ctx context.Context, adapter storage.Adapter, fi storage.FileInfo, tmpDir string, expected map[string]string, onBytes func(int64), partSize int64, concurrency int) error {
	storageName := filepath.Base(fi.Path)
	localPath := filepath.Join(tmpDir, storageName)
	out, err := os.Create(localPath) // #nosec G304 — tmpDir is vault-controlled
	if err != nil {
		return fmt.Errorf("creating local file %s: %w", localPath, err)
	}
	if err := storage.ParallelRangeDownload(ctx, adapter, fi.Path, out, fi.Size, partSize, concurrency, onBytes); err != nil {
		_ = out.Close()
		// The partial file is left in tmpDir on purpose: the caller's deferred
		// cleanup (RemoveAll on tmpDir) reclaims it, so no explicit remove here.
		return fmt.Errorf("downloading %s: %w", fi.Path, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", localPath, err)
	}
	if want, ok := expected[storageName]; ok {
		got, hashErr := sha256File(localPath)
		if hashErr != nil {
			return fmt.Errorf("verifying %s: %w", storageName, hashErr)
		}
		if got != want {
			return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", storageName, want, got)
		}
	}
	return nil
}

// downloadSequentialResumable streams a (possibly encrypted/compressed) object
// through the resumable reader and the decrypt→decompress→write pipeline,
// verifying the storage-content SHA-256 inline.
func (r *Runner) downloadSequentialResumable(ctx context.Context, adapter storage.Adapter, fi storage.FileInfo, tmpDir, passphrase, compression string, expected map[string]string, onBytes func(int64)) error {
	reader := storage.NewResumableReader(ctx, adapter, fi.Path, fi.Size, storage.DefaultRetryPolicy)
	defer reader.Close()

	storageHasher := sha256.New()
	hashingReader := io.TeeReader(&countingReader{reader: reader, onRead: onBytes}, storageHasher)

	dataReader := hashingReader
	localName := filepath.Base(fi.Path)
	if strings.HasSuffix(localName, ".age") && passphrase != "" {
		decrypted, decErr := crypto.DecryptReader(passphrase, hashingReader)
		if decErr != nil {
			return fmt.Errorf("decrypting %s: %w", fi.Path, decErr)
		}
		dataReader = decrypted
		localName = strings.TrimSuffix(localName, ".age")
	}

	decompressed, closeDecompress, restoredName, decmpErr := decompressStoredReader(dataReader, localName, compression)
	if decmpErr != nil {
		return fmt.Errorf("decompressing %s: %w", fi.Path, decmpErr)
	}
	defer closeDecompress() //nolint:errcheck
	localName = restoredName

	localPath := filepath.Join(tmpDir, localName)
	out, err := os.Create(localPath) // #nosec G304 — tmpDir is vault-controlled
	if err != nil {
		return fmt.Errorf("creating local file %s: %w", localPath, err)
	}
	if _, copyErr := io.Copy(out, decompressed); copyErr != nil {
		_ = out.Close()
		return fmt.Errorf("downloading %s: %w", fi.Path, copyErr)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", localPath, err)
	}

	storageName := filepath.Base(fi.Path)
	if want, ok := expected[storageName]; ok {
		got := hex.EncodeToString(storageHasher.Sum(nil))
		if got != want {
			return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", storageName, want, got)
		}
	}
	return nil
}

// sha256File computes the SHA-256 of a local file by streaming it.
func sha256File(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (r *Runner) restoreStagedItem(ctx context.Context, jobID int64, itemName, itemType, destination, tmpDir string, filePaths []string, reporter restoreProgressReporter, phaseStart, phaseEnd int) error {
	var handler engine.Handler
	var err error
	switch itemType {
	case "container":
		handler, err = engine.NewContainerHandler()
	case "vm":
		handler, err = engine.NewVMHandler()
	case "folder":
		handler, err = engine.NewFolderHandler()
	case "plugin":
		handler, err = engine.NewPluginHandler()
	case "zfs":
		handler, err = engine.NewZFSHandler()
	default:
		return fmt.Errorf("unknown item type: %s", itemType)
	}
	if err != nil {
		return fmt.Errorf("creating %s handler: %w", itemType, err)
	}

	progress := func(name string, pct int, msg string) {
		scaledPct := phaseStart + ((pct * (phaseEnd - phaseStart)) / 100)
		reporter.ItemName = name
		r.reportRestoreProgress(reporter, scaledPct, msg)
	}

	backupItem := engine.BackupItem{
		Name:     itemName,
		Type:     itemType,
		Settings: make(map[string]any),
	}

	// For folder items, load the path from job items settings.
	if itemType == "folder" {
		jobItems, itemsErr := r.db.GetJobItems(jobID)
		if itemsErr == nil {
			for _, ji := range jobItems {
				if ji.ItemName == itemName && ji.ItemType == "folder" {
					var s map[string]any
					if json.Unmarshal([]byte(ji.Settings), &s) == nil {
						backupItem.Settings = s
					}
					break
				}
			}
		}
	}

	// Override restore destination if specified.
	if destination != "" {
		backupItem.Settings["restore_destination"] = destination
	}

	// Per-item partial-restore filter (file picker in the restore wizard).
	// The engine handler is responsible for honouring this; non-supporting
	// handlers fall back to whole-archive restore.
	if len(filePaths) > 0 {
		backupItem.Settings["restore_file_paths"] = filePaths
	}

	restoreErr := handler.Restore(ctx, backupItem, tmpDir, progress)
	r.sendRestoreNotification(itemName, itemType, restoreErr)
	return restoreErr
}

// broadcast sends a JSON message to all connected WebSocket clients.
func (r *Runner) broadcast(data map[string]any) {
	if r.hub == nil {
		return
	}
	msg, err := json.Marshal(data)
	if err != nil {
		log.Printf("runner: failed to marshal broadcast: %v", err)
		return
	}
	r.hub.Broadcast(msg)
}

// Broadcast sends a JSON event to every connected WebSocket client. It is
// the public entry point used by API handlers (e.g., storage / job CRUD)
// to notify the UI that derived state — health summary, 3-2-1 compliance,
// recovery plan — needs to be re-fetched.
func (r *Runner) Broadcast(data map[string]any) {
	r.broadcast(data)
}

// broadcastQueueUpdate sends the current queue state to all WebSocket clients.
func (r *Runner) broadcastQueueUpdate() {
	r.queueMu.Lock()
	q := make([]QueueEntry, len(r.queue))
	copy(q, r.queue)
	r.queueMu.Unlock()

	r.broadcast(map[string]any{
		"type":  "queue_update",
		"queue": q,
	})
}

// logActivity writes an activity log entry and broadcasts it via WebSocket
// so connected clients can update in real time.
func (r *Runner) logActivity(level, category, message, details string) {
	entry := db.ActivityLogEntry{
		Level:    level,
		Category: category,
		Message:  message,
		Details:  details,
	}
	id, _ := r.db.CreateActivityLog(entry)
	entry.ID = id
	entry.CreatedAt = time.Now()
	r.broadcast(map[string]any{
		"type": "activity",
		"entry": map[string]any{
			"id":         entry.ID,
			"level":      entry.Level,
			"category":   entry.Category,
			"message":    entry.Message,
			"details":    entry.Details,
			"created_at": entry.CreatedAt.Format(time.RFC3339),
		},
	})
}

// sendNotification dispatches Unraid notifications based on job outcome and the
// job's notify_on preference ("always", "failure", "never").
// It also respects the global notifications_enabled setting.
func (r *Runner) sendNotification(job db.Job, status string, done, failed int, sizeBytes int64, durationSec int, failedNames []string) {
	// Check global notification switch first.
	globalEnabled, _ := r.db.GetSetting("notifications_enabled", "true")
	if globalEnabled != "true" {
		return
	}

	pref := job.NotifyOn
	if pref == "" {
		pref = "failure"
	}
	if pref == "never" {
		return
	}

	// Unraid notifications.
	switch status {
	case "completed":
		if pref == "always" {
			if err := notify.JobSuccess(job.Name, done, sizeBytes); err != nil {
				log.Printf("runner: notification error: %v", err)
			}
		}
	case "failed":
		if err := notify.JobFailed(job.Name, fmt.Sprintf("all %d items failed", done+failed)); err != nil {
			log.Printf("runner: notification error: %v", err)
		}
	case "partial":
		if err := notify.JobPartial(job.Name, done, failed); err != nil {
			log.Printf("runner: notification error: %v", err)
		}
	}

	// Discord notifications.
	webhookURL, _ := r.db.GetSetting("discord_webhook_url", "")
	discordPref, _ := r.db.GetSetting("discord_notify_on", "always")
	if webhookURL != "" && discordPref != "never" {
		shouldSend := discordPref == "always" || (discordPref == "failure" && status != "completed")
		if shouldSend {
			embed := r.buildDiscordEmbed(job.Name, status, done, failed, sizeBytes, durationSec, failedNames)
			opts := r.discordOptions(status)
			go func() {
				if err := notify.SendDiscord(webhookURL, embed, opts); err != nil {
					log.Printf("runner: discord notification error: %v", err)
				}
			}()
		}
	}
}

// discordOptions reads the Discord personalization settings (bot name/avatar and
// optional role mention) for a webhook notification. The role mention is only
// attached when a role ID is configured and the outcome matches the
// discord_mention_on preference, so a healthy backup doesn't ping anyone.
func (r *Runner) discordOptions(status string) notify.DiscordOptions {
	username, _ := r.db.GetSetting("discord_bot_username", "")
	avatarURL, _ := r.db.GetSetting("discord_bot_avatar_url", "")
	opts := notify.DiscordOptions{Username: username, AvatarURL: avatarURL}

	roleID, _ := r.db.GetSetting("discord_mention_role_id", "")
	mentionOn, _ := r.db.GetSetting("discord_mention_on", "never")
	if roleID != "" && shouldMentionDiscord(mentionOn, status) {
		opts.MentionRoleID = roleID
	}
	return opts
}

// shouldMentionDiscord decides whether to attach the role mention for a given
// outcome: "always" pings on every send, "failure" pings on failed/partial
// runs only, and anything else (including "never") never pings.
func shouldMentionDiscord(mentionOn, status string) bool {
	switch mentionOn {
	case "always":
		return true
	case "failure":
		return status != "completed"
	default:
		return false
	}
}

func (r *Runner) buildDiscordEmbed(jobName, status string, done, failed int, sizeBytes int64, durationSec int, failedNames []string) notify.DiscordEmbed {
	var title string
	var color int
	switch status {
	case "completed":
		title = "✅ Backup Completed"
		color = notify.ColorSuccess
	case "partial":
		title = "⚠️ Backup Partially Completed"
		color = notify.ColorWarning
	default:
		title = "❌ Backup Failed"
		color = notify.ColorDanger
	}

	fields := []notify.DiscordField{
		{Name: "Duration", Value: fmtDuration(durationSec), Inline: true},
		{Name: "Size", Value: fmtSize(sizeBytes), Inline: true},
	}
	if durationSec > 0 {
		fields = append(fields, notify.DiscordField{Name: "Speed", Value: fmtSize(sizeBytes/int64(durationSec)) + "/s", Inline: true})
	}
	fields = append(fields, notify.DiscordField{
		Name:   "Items",
		Value:  fmt.Sprintf("%d/%d succeeded", done, done+failed),
		Inline: true,
	})
	if len(failedNames) > 0 {
		names := strings.Join(failedNames, ", ")
		if len(names) > 200 {
			names = names[:200] + "..."
		}
		fields = append(fields, notify.DiscordField{Name: "Failed Items", Value: names})
	}

	return notify.DiscordEmbed{
		Title:       title,
		Description: jobName,
		Color:       color,
		Fields:      fields,
	}
}

func fmtDuration(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %dm", seconds/3600, (seconds%3600)/60)
}

func fmtSize(bytes int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)
	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.0f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// sendRestoreNotification sends an Unraid notification for restore outcomes.
func (r *Runner) sendRestoreNotification(itemName, itemType string, err error) {
	globalEnabled, _ := r.db.GetSetting("notifications_enabled", "true")
	if globalEnabled != "true" {
		return
	}

	if err != nil {
		r.logActivity("error", "restore", fmt.Sprintf("Restore failed: %s", itemName),
			structuredDetails(map[string]any{
				"item_name": itemName, "item_type": itemType,
				"error": err.Error(),
			}))
		if notifyErr := notify.Send(
			"Vault",
			fmt.Sprintf("Restore of '%s' failed", itemName),
			err.Error(),
			notify.ImportanceAlert,
		); notifyErr != nil {
			log.Printf("runner: restore notification error: %v", notifyErr)
		}
	} else {
		r.logActivity("info", "restore", fmt.Sprintf("Restore completed: %s", itemName),
			structuredDetails(map[string]any{
				"item_name": itemName, "item_type": itemType,
			}))
		if notifyErr := notify.Send(
			"Vault",
			fmt.Sprintf("Restore of '%s' completed", itemName),
			"Item was restored successfully",
			notify.ImportanceNormal,
		); notifyErr != nil {
			log.Printf("runner: restore notification error: %v", notifyErr)
		}
	}
}

// logLevelForStatus maps a job run status to an activity log level.
func logLevelForStatus(status string) string {
	switch status {
	case "completed":
		return "info"
	case "partial":
		return "warning"
	case "failed":
		return "error"
	case "cancelled":
		return "warning"
	default:
		return "info"
	}
}

// resolvePassphrase returns the encryption passphrase by trying (in order):
// 1. Sealed passphrase in DB (decrypted with server key).
// 2. Legacy plaintext passphrase in DB (migration compatibility).
func (r *Runner) resolvePassphrase() string {
	// Try sealed passphrase first.
	if sealed, _ := r.db.GetSetting("encryption_passphrase_sealed", ""); sealed != "" && len(r.serverKey) > 0 {
		passphrase, err := crypto.Unseal(r.serverKey, sealed)
		if err != nil {
			log.Printf("runner: failed to unseal passphrase: %v", err)
		} else {
			return passphrase
		}
	}

	// Fall back to legacy plaintext (will be cleaned up on next SetEncryption call).
	plaintext, _ := r.db.GetSetting("encryption_passphrase", "")
	return plaintext
}

// ResolvePassphrase exposes the configured encryption passphrase to other
// packages that need to read encrypted artifacts (e.g. the contents API
// handler that decrypts tar index sidecars). Returns the empty string when
// no passphrase is configured.
func (r *Runner) ResolvePassphrase() string {
	return r.resolvePassphrase()
}

// writeManifest writes a manifest.json file to storage containing metadata
// about the backup run: files, checksums, encryption status, and timestamps.
// This enables out-of-band recovery without access to the database.
//
// itemManifests carries per-item dedup manifest IDs (hex-encoded) when the
// destination has dedup enabled; pass nil/empty for non-dedup jobs. Without
// it, restoring an imported dedup backup on another instance can't resolve
// chunks because the manifest-ID linkage lives only in the local DB.
func (r *Runner) writeManifest(ctx context.Context, dest db.StorageDestination, basePath string, job db.Job, items []db.JobItem, runID int64, backupType string, itemsDone, itemsFailed int, totalSize int64, itemChecksums map[string]map[string]string, itemManifests map[string]string, timestamp string) {
	// Serialize items so a future import can recreate JobItems with the
	// correct type, name, and per-item settings (e.g. folder path,
	// container exclude_paths, ZFS dataset). Without this, importing a
	// backup from a different Vault installation produces an empty job
	// with no restorable items.
	manifestItems := make([]map[string]any, 0, len(items))
	for _, it := range items {
		entry := map[string]any{
			"name": it.ItemName,
			"type": it.ItemType,
			"id":   it.ItemID,
		}
		if it.Settings != "" {
			entry["settings"] = json.RawMessage(it.Settings)
		}
		manifestItems = append(manifestItems, entry)
	}

	manifest := map[string]any{
		"version":           manifestVersionCurrent,
		"job_name":          job.Name,
		"job_id":            job.ID,
		"run_id":            runID,
		"backup_type":       backupType,
		"backup_type_chain": job.BackupTypeChain,
		"encryption":        job.Encryption,
		"compression":       job.Compression,
		"retention_count":   job.RetentionCount,
		"retention_days":    job.RetentionDays,
		"container_mode":    job.ContainerMode,
		"vm_mode":           job.VMMode,
		"notify_on":         job.NotifyOn,
		"verify_backup":     job.VerifyBackup,
		"items":             manifestItems,
		"items_done":        itemsDone,
		"items_failed":      itemsFailed,
		"size_bytes":        totalSize,
		"verified":          job.VerifyBackup,
		"timestamp":         timestamp,
		"created_at":        time.Now().UTC().Format(time.RFC3339),
	}
	if len(itemChecksums) > 0 {
		manifest["checksums"] = itemChecksums
	}
	if len(itemManifests) > 0 {
		manifest["item_manifests"] = itemManifests
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		log.Printf("runner: failed to marshal manifest: %v", err)
		return
	}

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		log.Printf("runner: failed to create adapter for manifest: %v", err)
		return
	}
	defer storage.CloseAdapter(adapter)
	// Closing the adapter on cancellation aborts an in-flight write so a
	// timed-out finalization step's goroutine exits instead of leaking.
	stopOnCancel := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stopOnCancel()

	manifestPath := filepath.Join(basePath, "manifest.json")
	if err := adapter.Write(manifestPath, strings.NewReader(string(data))); err != nil {
		log.Printf("runner: failed to write manifest to %s: %v", manifestPath, err)
	}
}

// enforceRetentionLTR deletes restore points that are not protected by the
// long-term retention policy. Chain-ancestor protection mirrors the
// classic enforceRetention path: any parent restore point still required by
// a kept incremental/differential survives the sweep.
func (r *Runner) enforceRetentionLTR(ctx context.Context, dest db.StorageDestination, jobID int64, policy LTRPolicy) {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		log.Printf("runner: failed to create adapter for LTR retention cleanup: %v", err)
	}
	defer storage.CloseAdapter(adapter)
	stopOnCancel := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stopOnCancel()

	allRestorePoints, err := r.db.ListRestorePoints(jobID)
	if err != nil {
		log.Printf("runner: failed to list restore points for job %d: %v", jobID, err)
		return
	}

	protected := ltrProtectedRestorePointIDs(allRestorePoints, policy, time.Local)
	log.Printf("runner: LTR retention for job %d: keeping %d of %d restore points (policy: latest=%d daily=%d weekly=%d monthly=%d yearly=%d)",
		jobID, len(protected), len(allRestorePoints),
		policy.KeepLatest, policy.KeepDaily, policy.KeepWeekly, policy.KeepMonthly, policy.KeepYearly)
	for _, rp := range allRestorePoints {
		if ctx.Err() != nil {
			log.Printf("runner: LTR retention sweep for job %d cancelled; remaining restore points untouched until the next run", jobID)
			return
		}
		if _, ok := protected[rp.ID]; ok {
			continue
		}
		r.deleteVMCheckpointsForRP(rp)
		if adapter != nil && rp.StoragePath != "" {
			r.deleteStorageDir(adapter, rp.StoragePath)
		}
		if err := r.db.DeleteRestorePoint(rp.ID); err != nil {
			log.Printf("runner: failed to delete restore point %d for job %d: %v", rp.ID, jobID, err)
		}
	}
}

// enforceRetention deletes old restore points and their storage files.
// It handles both count-based and time-based (days) retention. Count-based
// retention runs first, then time-based cleanup removes any remaining
// restore points older than the specified days.
func (r *Runner) enforceRetention(ctx context.Context, dest db.StorageDestination, jobID int64, keepCount, keepDays int) {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		log.Printf("runner: failed to create adapter for retention cleanup: %v", err)
	}
	defer storage.CloseAdapter(adapter)
	stopOnCancel := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stopOnCancel()

	allRestorePoints, err := r.db.ListRestorePoints(jobID)
	if err != nil {
		log.Printf("runner: failed to list restore points for job %d: %v", jobID, err)
		return
	}

	protected := protectedRestorePointIDs(allRestorePoints, keepCount, keepDays, time.Now())
	for _, rp := range allRestorePoints {
		if ctx.Err() != nil {
			log.Printf("runner: retention sweep for job %d cancelled; remaining restore points untouched until the next run", jobID)
			return
		}
		if _, ok := protected[rp.ID]; ok {
			continue
		}
		// Best-effort cleanup of libvirt checkpoints associated with VM
		// items in this restore point. Failures are logged and ignored
		// so retention continues to delete storage and the DB row.
		r.deleteVMCheckpointsForRP(rp)
		if adapter != nil && rp.StoragePath != "" {
			r.deleteStorageDir(adapter, rp.StoragePath)
		}
		if err := r.db.DeleteRestorePoint(rp.ID); err != nil {
			log.Printf("runner: failed to delete restore point %d for job %d: %v", rp.ID, jobID, err)
		}
	}
}

// deleteVMCheckpointsForRP removes any libvirt checkpoints recorded in the
// given restore point's metadata. It is safe to call for non-VM restore
// points and on systems without libvirt — both result in no-ops.
func (r *Runner) deleteVMCheckpointsForRP(rp db.RestorePoint) {
	if rp.Metadata == "" {
		return
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(rp.Metadata), &meta); err != nil {
		return
	}
	raw, ok := meta["vm_checkpoints"].(map[string]any)
	if !ok || len(raw) == 0 {
		return
	}
	handler, err := engine.NewVMHandler()
	if err != nil {
		log.Printf("runner: skipping VM checkpoint cleanup for RP %d: %v", rp.ID, err)
		return
	}
	defer handler.Close()
	for domainName, v := range raw {
		cp, _ := v.(string)
		if cp == "" {
			continue
		}
		if err := handler.DeleteCheckpoint(domainName, cp); err != nil {
			log.Printf("runner: failed to delete checkpoint %s for VM %s (RP %d): %v", cp, domainName, rp.ID, err)
		}
	}
}

// deleteStorageDir recursively deletes all files under a storage path prefix.
func (r *Runner) deleteStorageDir(adapter storage.Adapter, prefix string) {
	_ = r.DeleteStorageDir(adapter, prefix)
}

// DeleteStorageDir recursively deletes all files under a storage path prefix,
// then removes the directory itself. It returns a joined error describing every
// list/delete operation that failed (nil when the subtree was removed cleanly)
// so callers that report completion status — e.g. the async cleanup goroutines
// — can tell success from partial failure rather than always claiming success.
// Errors are also logged for operators tailing the daemon log.
func (r *Runner) DeleteStorageDir(adapter storage.Adapter, prefix string) error {
	files, err := adapter.List(prefix)
	if err != nil {
		// A directory that no longer exists is already clean — nothing to
		// delete. Treating this as a hard failure made job/restore-point
		// cleanup report "files may remain on storage" when a path had been
		// removed already or never existed (e.g. WebDAV PROPFIND 404). (#143)
		if storage.IsNotExist(err) {
			return nil
		}
		log.Printf("runner: failed to list storage dir %s for cleanup: %v", prefix, err)
		return fmt.Errorf("list %s: %w", prefix, err)
	}

	var errs []error
	for _, fi := range files {
		if fi.IsDir {
			if e := r.DeleteStorageDir(adapter, fi.Path); e != nil {
				errs = append(errs, e)
			}
			continue
		}
		if e := adapter.Delete(fi.Path); e != nil {
			log.Printf("runner: failed to delete storage file %s: %v", fi.Path, e)
			errs = append(errs, fmt.Errorf("delete %s: %w", fi.Path, e))
		}
	}

	// Remove the now-empty directory itself.
	if e := adapter.Delete(prefix); e != nil {
		log.Printf("runner: failed to remove storage directory %s: %v", prefix, e)
		errs = append(errs, fmt.Errorf("delete %s: %w", prefix, e))
	}
	r.sweepEmptyParents(adapter, prefix, "")
	return errors.Join(errs...)
}

// sweepEmptyParents removes now-empty directories from start's parent upward,
// stopping at root or the first non-empty directory. No-op for adapters that do
// not implement the optional dir-removal interface (object stores, WebDAV).
func (r *Runner) sweepEmptyParents(adapter storage.Adapter, start, root string) {
	dr, ok := adapter.(interface{ RemoveEmptyDir(string) error })
	if !ok {
		return
	}
	dir := path.Dir(start)
	for dir != "" && dir != "." && dir != "/" && dir != root {
		if err := dr.RemoveEmptyDir(dir); err != nil {
			return // non-empty or unsupported: stop walking up
		}
		dir = path.Dir(dir)
	}
}

// CleanupJobStorage deletes all backup files on storage for the given job.
// It fetches all restore points, removes their storage directories, then
// removes the top-level job directory (<job_name>/).
func (r *Runner) CleanupJobStorage(jobID int64) error {
	job, err := r.db.GetJob(jobID)
	if err != nil {
		return fmt.Errorf("getting job %d: %w", jobID, err)
	}

	dest, err := r.db.GetStorageDestination(job.StorageDestID)
	if err != nil {
		return fmt.Errorf("getting storage destination for job %d: %w", jobID, err)
	}

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return fmt.Errorf("creating storage adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)

	rps, err := r.db.ListRestorePoints(jobID)
	if err != nil {
		return fmt.Errorf("listing restore points for job %d: %w", jobID, err)
	}

	for _, rp := range rps {
		if rp.StoragePath != "" {
			_ = r.DeleteStorageDir(adapter, rp.StoragePath) // best-effort; errors are logged inside
		}
	}

	// Also clean up the top-level job directory.
	jobDir := job.Name
	_ = r.DeleteStorageDir(adapter, jobDir) // best-effort; errors are logged inside

	return nil
}

// CleanupJobStorageAsync deletes a job's backup files on remote storage in a
// background goroutine, then broadcasts a job_cleanup_complete (or
// job_cleanup_failed) WebSocket event. Cleanup of a large backup on a slow
// remote (e.g. ~30 GB on a Hetzner Storage Box) can take far longer than an
// HTTP client is willing to wait, which previously surfaced as a spurious
// "daemon unavailable" error even though the server eventually returned 204
// (issue #111). The caller deletes the DB row first and passes the details
// here because the job (and its restore points) are gone by the time this runs.
func (r *Runner) CleanupJobStorageAsync(jobID int64, jobName string, dest db.StorageDestination, storagePaths []string) {
	go func() {
		adapter, err := storage.NewAdapter(dest.Type, dest.Config)
		if err != nil {
			log.Printf("runner: async cleanup for job %d: creating storage adapter: %v", jobID, err)
			r.broadcast(map[string]any{
				"type": "job_cleanup_failed", "job_id": jobID, "job_name": jobName, "error": err.Error(),
			})
			return
		}
		defer storage.CloseAdapter(adapter)

		// The recorded restore-point paths are authoritative backup data; a
		// failure to delete any of them is a real cleanup failure.
		var errs []error
		for _, p := range storagePaths {
			if p != "" {
				if e := r.DeleteStorageDir(adapter, p); e != nil {
					errs = append(errs, e)
				}
			}
		}
		// Sweep the top-level job directory to catch anything the per-restore-
		// point paths missed (orphaned partial uploads, sidecar dirs, etc.).
		// This is best-effort: a job that never wrote a top-level dir would
		// surface a benign "not found", so its error does not fail the cleanup.
		_ = r.DeleteStorageDir(adapter, jobName)

		// Dedup destinations keep backup data in a shared content-addressed
		// repo (_vault/), so deleting a job's manifests above leaves its chunks
		// behind. Reclaim them: if this was the last job on the destination the
		// whole repo is orphaned and removed outright; otherwise run GC to free
		// only the chunks no longer referenced by a surviving job. (issue #143)
		if dest.DedupEnabled {
			r.reclaimDedupAfterJobDelete(adapter, jobID, dest, &errs)
		}

		if err := errors.Join(errs...); err != nil {
			log.Printf("runner: async cleanup for job %d (%q) failed: %v", jobID, jobName, err)
			// Activity log makes the failure durably visible: cleanup can finish
			// minutes after the delete request, when no page may be listening.
			r.logActivity("error", "system",
				fmt.Sprintf("Failed to delete backup files for deleted job %q — files may remain on storage", jobName),
				err.Error())
			r.broadcast(map[string]any{
				"type": "job_cleanup_failed", "job_id": jobID, "job_name": jobName, "error": err.Error(),
			})
			return
		}

		log.Printf("runner: async cleanup for job %d (%q) complete", jobID, jobName)
		r.broadcast(map[string]any{
			"type": "job_cleanup_complete", "job_id": jobID, "job_name": jobName,
		})
	}()
}

// reclaimDedupAfterJobDelete frees dedup data left behind by a just-deleted
// job. The job row and its restore points are already gone, so:
//   - if no jobs reference the destination anymore, the entire _vault repo is
//     orphaned: remove it and clear the destination's dedup index rows so a
//     future backup re-initialises cleanly;
//   - otherwise other jobs still share the repo, so run GC to reclaim only the
//     chunks no longer referenced by a surviving restore point.
//
// Repo-removal errors are appended to errs (genuine cleanup failures); GC
// reports its own outcome via the dedup_gc_complete broadcast and the log.
func (r *Runner) reclaimDedupAfterJobDelete(adapter storage.Adapter, jobID int64, dest db.StorageDestination, errs *[]error) {
	remaining, err := r.db.CountJobsByStorageDestID(dest.ID)
	if err != nil {
		log.Printf("runner: dedup cleanup: counting jobs for dest %d: %v", dest.ID, err)
		return
	}
	if remaining > 0 {
		// Other jobs still share the repo — reclaim only orphaned chunks.
		r.RunDedupGC(dest, fmt.Sprintf("job-delete-%d", jobID))
		return
	}
	// Last dedup job on this destination — the whole repo is orphaned.
	if e := r.DeleteStorageDir(adapter, dedup.RepoRoot); e != nil {
		*errs = append(*errs, fmt.Errorf("remove dedup repo: %w", e))
	}
	// Surface a failure to clear the dedup index too: leaving stale pack/chunk
	// rows behind would corrupt the repo re-init on the next backup, so it must
	// not fail silently.
	if e := r.db.DeleteDedupState(dest.ID); e != nil {
		*errs = append(*errs, fmt.Errorf("clear dedup index state: %w", e))
	}
}

// CleanupRestorePointStorageAsync deletes a single restore point's files on
// remote storage in a background goroutine, mirroring CleanupJobStorageAsync.
// Deleting a large restore point on a slow remote suffers the same HTTP-timeout
// problem as job deletion (issue #111), so the caller removes the DB row first
// and the remote sweep happens here.
func (r *Runner) CleanupRestorePointStorageAsync(jobID, rpID int64, dest db.StorageDestination, storagePath string) {
	go func() {
		adapter, err := storage.NewAdapter(dest.Type, dest.Config)
		if err != nil {
			log.Printf("runner: async cleanup for restore point %d: creating storage adapter: %v", rpID, err)
			r.broadcast(map[string]any{
				"type": "restore_point_cleanup_failed", "job_id": jobID, "restore_point_id": rpID, "error": err.Error(),
			})
			return
		}
		defer storage.CloseAdapter(adapter)

		if storagePath != "" {
			if e := r.DeleteStorageDir(adapter, storagePath); e != nil {
				log.Printf("runner: async cleanup for restore point %d failed: %v", rpID, e)
				r.logActivity("error", "system",
					fmt.Sprintf("Failed to delete backup files for deleted restore point %d — files may remain on storage", rpID),
					e.Error())
				r.broadcast(map[string]any{
					"type": "restore_point_cleanup_failed", "job_id": jobID, "restore_point_id": rpID, "error": e.Error(),
				})
				return
			}
		}

		log.Printf("runner: async cleanup for restore point %d complete", rpID)
		r.broadcast(map[string]any{
			"type": "restore_point_cleanup_complete", "job_id": jobID, "restore_point_id": rpID,
		})
	}()
}

// CleanupStorageDestination deletes all Vault backup files from a storage
// destination by removing all top-level job directories.
func (r *Runner) CleanupStorageDestination(dest db.StorageDestination) error {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return fmt.Errorf("creating storage adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)

	// List and delete all top-level job directories.
	topEntries, listErr := adapter.List(".")
	if listErr == nil {
		for _, entry := range topEntries {
			if entry.IsDir {
				_ = r.DeleteStorageDir(adapter, entry.Path) // best-effort; errors are logged inside
			}
		}
	}
	return nil
}

// manifestVersionCurrent is the manifest.json schema version written by
// writeManifest and the highest version this daemon knows how to import.
// Manifests with a higher version (written by a newer Vault) are skipped
// with a clear log line instead of being silently mis-parsed as legacy.
const manifestVersionCurrent = 2

// manifestVersionTooNew reports whether a discovered manifest declares a
// schema version newer than this daemon supports. A missing or non-numeric
// version means a legacy (v1) manifest and is always accepted.
func manifestVersionTooNew(m map[string]any) (int, bool) {
	v, ok := m["version"].(float64)
	return int(v), ok && int(v) > manifestVersionCurrent
}

// ScanStorageManifests scans a storage destination for backup manifests
// and returns metadata about each discovered backup run.
func (r *Runner) ScanStorageManifests(dest db.StorageDestination) ([]map[string]any, error) {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return nil, fmt.Errorf("creating storage adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)

	// List all entries under the storage root.
	topEntries, err := adapter.List(".")
	if err != nil {
		return nil, fmt.Errorf("listing storage root: %w", err)
	}

	var manifests []map[string]any

	// Walk <job_name>/<run_id>_<timestamp>/manifest.json.
	for _, jobDir := range topEntries {
		if !jobDir.IsDir {
			continue
		}
		runEntries, err := adapter.List(jobDir.Path)
		if err != nil {
			log.Printf("runner: scan: failed to list %s: %v", jobDir.Path, err)
			continue
		}
		for _, runDir := range runEntries {
			if !runDir.IsDir {
				continue
			}
			manifestPath := runDir.Path + "/manifest.json"
			rc, err := adapter.Read(manifestPath)
			if err != nil {
				continue // No manifest — skip.
			}
			data, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				continue
			}
			var manifest map[string]any
			if err := json.Unmarshal(data, &manifest); err != nil {
				continue
			}
			if v, tooNew := manifestVersionTooNew(manifest); tooNew {
				log.Printf("runner: scan: skipping %s: manifest version %d is newer than this daemon supports (max %d) — upgrade Vault to import this backup", manifestPath, v, manifestVersionCurrent)
				continue
			}
			// Add the storage path so the import handler knows where this backup lives.
			manifest["storage_path"] = runDir.Path
			manifests = append(manifests, manifest)
		}
	}

	return manifests, nil
}

// ScanAppdataBackups scans a storage destination for backup directories
// created by Commifreak's unraid-appdata.backup plugin. These directories
// follow the naming convention ab_YYYYMMDD_HHMMSS and contain .tar.gz
// archives (one per container) and optionally cube-*.zip flash backups.
func (r *Runner) ScanAppdataBackups(dest db.StorageDestination, basePath string) ([]map[string]any, error) {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return nil, fmt.Errorf("creating storage adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)

	// List entries in the base path (root if empty).
	topEntries, err := adapter.List(basePath)
	if err != nil {
		return nil, fmt.Errorf("listing root directory: %w", err)
	}

	var manifests []map[string]any

	for _, entry := range topEntries {
		if !entry.IsDir {
			continue
		}

		// Extract directory name from path.
		dirName := filepath.Base(entry.Path)
		if !strings.HasPrefix(dirName, "ab_") {
			continue
		}

		// Parse timestamp from directory name: ab_YYYYMMDD_HHMMSS
		createdAt := parseAppdataTimestamp(dirName)

		// List files inside this ab_ directory.
		files, err := adapter.List(entry.Path)
		if err != nil {
			log.Printf("runner: scan appdata: failed to list %s: %v", entry.Path, err)
			continue
		}

		for _, f := range files {
			if f.IsDir {
				continue
			}

			fileName := filepath.Base(f.Path)

			var jobName, compression string

			switch {
			case strings.HasSuffix(fileName, ".tar.gz"):
				jobName = strings.TrimSuffix(fileName, ".tar.gz")
				compression = "gzip"
			case strings.HasSuffix(fileName, ".zip"):
				// Flash backups are ZIP files named <hostname>-<date>.zip.
				// The hostname varies per system (cube, tower, unraid, etc.),
				// so match any .zip file — container backups always use .tar.gz.
				jobName = "flash-backup"
				compression = "zip"
			default:
				// Skip non-backup files (.xml, .json, .log, etc.)
				continue
			}

			manifests = append(manifests, map[string]any{
				"source":       "appdata.backup",
				"job_name":     jobName,
				"storage_path": f.Path,
				"backup_type":  "full",
				"compression":  compression,
				"size_bytes":   float64(f.Size),
				"created_at":   createdAt,
			})
		}
	}

	return manifests, nil
}

// parseAppdataTimestamp parses a directory name like ab_20260304_040001
// into an ISO 8601 timestamp string. Returns empty string on parse failure.
func parseAppdataTimestamp(dirName string) string {
	// Expected format: ab_YYYYMMDD_HHMMSS
	parts := strings.SplitN(dirName, "_", 3)
	if len(parts) != 3 {
		return ""
	}
	dateStr := parts[1] // YYYYMMDD
	timeStr := parts[2] // HHMMSS

	if len(dateStr) != 8 || len(timeStr) != 6 {
		return ""
	}

	t, err := time.Parse("20060102150405", dateStr+timeStr)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ImportBackups creates job and restore point records from previously
// discovered manifests. For each manifest, it finds or creates the job
// by name, creates a synthetic job run, and creates a restore point
// referencing the original storage path. When a manifest is from the
// native Vault format (writeManifest) it also propagates job-level
// settings (retention, modes, verify) and recreates the per-item rows
// (JobItems) so the restore wizard can list snapshots immediately.
func (r *Runner) ImportBackups(storageDestID int64, backups []map[string]any) (int, error) {
	imported := 0

	for _, b := range backups {
		jobName, _ := b["job_name"].(string)
		storagePath, _ := b["storage_path"].(string)
		backupType, _ := b["backup_type"].(string)
		sizeBytes, _ := b["size_bytes"].(float64)
		compression, _ := b["compression"].(string)
		encryption, _ := b["encryption"].(string)

		if jobName == "" || storagePath == "" {
			continue
		}
		if v, tooNew := manifestVersionTooNew(b); tooNew {
			log.Printf("runner: import: skipping %q (%s): manifest version %d is newer than this daemon supports (max %d) — upgrade Vault to import this backup", jobName, storagePath, v, manifestVersionCurrent)
			continue
		}
		if backupType == "" {
			backupType = "full"
		}

		// Find or create the job.
		job, err := r.db.GetJobByName(jobName)
		jobIsNew := false
		if err != nil {
			// Job doesn't exist — populate from the manifest where
			// possible so retention, container/VM modes, and verify
			// settings survive across installations.
			chain, _ := b["backup_type_chain"].(string)
			if chain == "" {
				chain = "full"
			}
			retentionCount := 7
			if v, ok := b["retention_count"].(float64); ok && v > 0 {
				retentionCount = int(v)
			}
			retentionDays := 30
			if v, ok := b["retention_days"].(float64); ok && v >= 0 {
				retentionDays = int(v)
			}
			containerMode, _ := b["container_mode"].(string)
			if containerMode == "" {
				containerMode = "one_by_one"
			}
			vmMode, _ := b["vm_mode"].(string)
			notifyOn, _ := b["notify_on"].(string)
			if notifyOn == "" {
				notifyOn = "failure"
			}
			verifyBackup := true
			if v, ok := b["verify_backup"].(bool); ok {
				verifyBackup = v
			}

			job = db.Job{
				Name:            jobName,
				Enabled:         false,
				BackupTypeChain: chain,
				RetentionCount:  retentionCount,
				RetentionDays:   retentionDays,
				Compression:     compression,
				Encryption:      encryption,
				ContainerMode:   containerMode,
				VMMode:          vmMode,
				NotifyOn:        notifyOn,
				VerifyBackup:    verifyBackup,
				StorageDestID:   storageDestID,
			}
			job.ID, err = r.db.CreateJob(job)
			if err != nil {
				log.Printf("runner: import: failed to create job %q: %v", jobName, err)
				continue
			}
			jobIsNew = true
		}

		// Recreate JobItems so the restore wizard can list items.
		// Only populate for newly-created jobs; if the job already
		// existed locally, trust the user's current configuration.
		if jobIsNew {
			itemRows := buildImportJobItems(b)
			for _, item := range itemRows {
				item.JobID = job.ID
				if _, err := r.db.AddJobItem(item); err != nil {
					log.Printf("runner: import: failed to add item %q for job %q: %v", item.ItemName, jobName, err)
				}
			}
		}

		// Check if a restore point with this storage_path already exists.
		existingRPs, err := r.db.ListRestorePoints(job.ID)
		if err == nil {
			duplicate := false
			for _, rp := range existingRPs {
				if rp.StoragePath == storagePath {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
		}

		// Create a synthetic job run.
		// Manifest schema (writeManifest):
		//   items        — array of {name,type,id,...}; len = total items
		//   items_done   — int, succeeded
		//   items_failed — int, failed
		//   size_bytes   — int64, total archive size
		// We populate items_total from len(items) (or fall back to
		// items_done if the manifest predates v2), items_done /
		// items_failed / size_bytes from their respective fields, so
		// the Backup & Restore History page shows the original
		// success counts and contributes to the Total Size card —
		// not "0/14 items" with "—" size.
		itemsDone := 0
		if v, ok := b["items_done"].(float64); ok {
			itemsDone = int(v)
		}
		itemsFailed := 0
		if v, ok := b["items_failed"].(float64); ok {
			itemsFailed = int(v)
		}
		itemsTotal := itemsDone
		if rawItems, ok := b["items"].([]any); ok && len(rawItems) > 0 {
			itemsTotal = len(rawItems)
		}
		runSizeBytes := int64(sizeBytes)

		// Use the manifest's original backup time as both started_at
		// and completed_at so the History page shows when the backup
		// actually ran — not when it was imported. Prefer ISO-8601
		// `created_at` (RFC3339), then the legacy `timestamp`
		// (`2006-01-02_150405`); fall back to time.Now() if both are
		// missing.
		runTime := time.Time{}
		if v, ok := b["created_at"].(string); ok && v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				runTime = t
			}
		}
		if runTime.IsZero() {
			if v, ok := b["timestamp"].(string); ok && v != "" {
				if t, err := time.Parse("2006-01-02_150405", v); err == nil {
					runTime = t
				}
			}
		}
		if runTime.IsZero() {
			runTime = time.Now()
		}

		run := db.JobRun{
			JobID:       job.ID,
			Status:      "imported",
			BackupType:  backupType,
			ItemsTotal:  itemsTotal,
			ItemsDone:   itemsDone,
			ItemsFailed: itemsFailed,
			SizeBytes:   runSizeBytes,
		}
		runID, err := r.db.CreateImportedJobRun(run, runTime)
		if err != nil {
			log.Printf("runner: import: failed to create job run for %q: %v", jobName, err)
			continue
		}

		// Build metadata JSON from manifest. The native runner stores
		// `items` as a count (int) on restore points, but Vault
		// manifests carry `items` as an array of {name,type,id,...}
		// objects. Normalize to the runner's restore-point shape so
		// the restore wizard's `meta.items` rendering doesn't end up
		// stringifying an array of objects.
		rpMeta := map[string]any{
			"items":       itemsTotal,
			"job_name":    jobName,
			"backup_type": backupType,
			"imported":    true,
		}
		if itemsFailed > 0 {
			rpMeta["items_failed"] = itemsFailed
		}
		if v, ok := b["item_sizes"].(map[string]any); ok && len(v) > 0 {
			rpMeta["item_sizes"] = v
		}
		if v, ok := b["checksums"].(map[string]any); ok && len(v) > 0 {
			rpMeta["checksums"] = v
		}
		if v, ok := b["verified"].(bool); ok {
			rpMeta["verified"] = v
		}
		if rawItems, ok := b["items"].([]any); ok {
			// Keep the original manifest items under a non-conflicting
			// key for diagnostics / future use without breaking the UI.
			rpMeta["manifest_items"] = rawItems
		}
		// For dedup backups the manifest carries item_manifests: a map of
		// item name -> hex-encoded dedup manifest ID. Propagate it so the
		// restore path's resolveManifestID lookup succeeds for imported
		// dedup restore points. Without this, the row would appear in the
		// UI but restore would fail (chunks unreachable, IDs lost).
		itemManifests := map[string]string{}
		if v, ok := b["item_manifests"].(map[string]any); ok {
			for name, raw := range v {
				if hexID, ok := raw.(string); ok && hexID != "" {
					itemManifests[name] = hexID
				}
			}
		}
		if len(itemManifests) > 0 {
			rpMeta["item_manifests"] = itemManifests
		}
		metaBytes, _ := json.Marshal(rpMeta)

		rp := db.RestorePoint{
			JobRunID:    runID,
			JobID:       job.ID,
			BackupType:  backupType,
			StoragePath: storagePath,
			Metadata:    string(metaBytes),
			SizeBytes:   int64(sizeBytes),
		}
		// Single-item dedup jobs also get the manifest ID promoted onto
		// the restore-point row so resolveManifestID can hit the fast
		// path without parsing JSON.
		if len(itemManifests) == 1 {
			for _, hexID := range itemManifests {
				if decoded, err := hex.DecodeString(hexID); err == nil && len(decoded) == 32 {
					rp.ManifestID = decoded
				}
			}
		}
		rpID, err := r.db.CreateRestorePoint(rp)
		if err != nil {
			log.Printf("runner: import: failed to create restore point for %q: %v", jobName, err)
			continue
		}
		if rp.ManifestID != nil {
			if err := r.db.SetRestorePointManifestID(rpID, rp.ManifestID); err != nil {
				log.Printf("runner: import: failed to persist manifest_id for rp %d: %v", rpID, err)
			}
		}

		imported++
	}

	if imported > 0 {
		// Notify connected clients so the History and Restore pages
		// refresh after a successful import without requiring a manual
		// page reload.
		r.broadcast(map[string]any{
			"type":     "import_completed",
			"imported": imported,
		})
	}

	return imported, nil
}

// buildImportJobItems extracts JobItem rows from an import manifest. It
// supports three sources, in order of preference:
//
//  1. Native Vault manifests v2+ with an explicit "items" array containing
//     {name, type, settings, id} per item.
//  2. Older Vault manifests without an items array — falls back to the
//     "item_sizes" map (item names only, type defaulted to "container").
//  3. appdata.backup imports (source == "appdata.backup") — produces a
//     single container item matching the job/container name.
//
// Returns an empty slice if no items can be inferred; callers must treat
// that as "skip item creation" rather than an error.
func buildImportJobItems(b map[string]any) []db.JobItem {
	if rawItems, ok := b["items"].([]any); ok && len(rawItems) > 0 {
		items := make([]db.JobItem, 0, len(rawItems))
		for _, raw := range rawItems {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			if name == "" {
				continue
			}
			itemType, _ := m["type"].(string)
			if itemType == "" {
				itemType = "container"
			}
			itemID, _ := m["id"].(string)
			settingsJSON := "{}"
			if s, ok := m["settings"]; ok && s != nil {
				if sb, err := json.Marshal(s); err == nil {
					settingsJSON = string(sb)
				}
			}
			items = append(items, db.JobItem{
				ItemType: itemType,
				ItemName: name,
				ItemID:   itemID,
				Settings: settingsJSON,
			})
		}
		if len(items) > 0 {
			return items
		}
	}

	// Fallback: appdata.backup produces one container per file.
	if source, _ := b["source"].(string); source == "appdata.backup" {
		jobName, _ := b["job_name"].(string)
		if jobName != "" {
			return []db.JobItem{{
				ItemType: "container",
				ItemName: jobName,
				Settings: "{}",
			}}
		}
	}

	// Fallback for legacy Vault manifests: derive names from item_sizes.
	if sizes, ok := b["item_sizes"].(map[string]any); ok {
		items := make([]db.JobItem, 0, len(sizes))
		for name := range sizes {
			if name == "" {
				continue
			}
			items = append(items, db.JobItem{
				ItemType: "container",
				ItemName: name,
				Settings: "{}",
			})
		}
		return items
	}

	return nil
}

// vmCheckpointFromRPMeta returns the libvirt checkpoint name recorded for the
// given VM item in a restore point's metadata JSON. Returns "" when the
// metadata does not include a checkpoint for that item.
func vmCheckpointFromRPMeta(metadata, itemName string) string {
	if metadata == "" || itemName == "" {
		return ""
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		return ""
	}
	raw, ok := meta["vm_checkpoints"].(map[string]any)
	if !ok {
		return ""
	}
	cp, _ := raw[itemName].(string)
	return cp
}

// parseItemChecksums extracts the SHA-256 checksums for a specific item from
// restore point metadata JSON. Returns filename→hash map, or empty if unavailable.
func (r *Runner) parseItemChecksums(metadata, itemName string) map[string]string {
	var meta map[string]any
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		return nil
	}

	checksums, ok := meta["checksums"]
	if !ok {
		return nil
	}

	allChecksums, ok := checksums.(map[string]any)
	if !ok {
		return nil
	}

	itemChecksums, ok := allChecksums[itemName]
	if !ok {
		return nil
	}

	fileChecksums, ok := itemChecksums.(map[string]any)
	if !ok {
		return nil
	}

	result := make(map[string]string, len(fileChecksums))
	for fileName, hash := range fileChecksums {
		if h, ok := hash.(string); ok {
			result[fileName] = h
		}
	}
	return result
}

// structuredDetails marshals data to a JSON string for activity log details.
// Falls back to fmt.Sprint if marshalling fails.
func structuredDetails(data any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprint(data)
	}
	return string(b)
}

// jobItemNames extracts item names from a slice of job items.
func jobItemNames(items []db.JobItem) []string {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = item.ItemName
	}
	return names
}

// backupTypeResult contains the resolved backup type and parent restore point.
type backupTypeResult struct {
	// BackupType is the actual type for this run ("full", "incremental", or "differential").
	BackupType string
	// ParentRP is the reference restore point (nil for full backups).
	ParentRP *db.RestorePoint
}

// resolveBackupType determines the actual backup type for this run based on
// the job's backup_type_chain configuration and the history of past restores.
//
//   - "full": always returns "full"
//   - "incremental": returns "full" if no previous restore points exist,
//     otherwise "incremental" with the most recent restore point as parent
//   - "differential": returns "full" if no previous full exists,
//     otherwise "differential" with the last full as parent
func (r *Runner) resolveBackupType(job db.Job) backupTypeResult {
	chain := job.BackupTypeChain
	if chain == "" || chain == "full" {
		return backupTypeResult{BackupType: "full"}
	}

	switch chain {
	case "incremental":
		lastRP, err := r.db.GetLastRestorePoint(job.ID)
		if err != nil {
			// No previous restore points — force a full backup.
			log.Printf("runner: no previous restore point for job %d, creating full backup", job.ID)
			return backupTypeResult{BackupType: "full"}
		}
		return backupTypeResult{BackupType: "incremental", ParentRP: &lastRP}

	case "differential":
		lastFull, err := r.db.GetLastRestorePointByType(job.ID, "full")
		if err != nil {
			log.Printf("runner: no previous full restore point for job %d, creating full backup", job.ID)
			return backupTypeResult{BackupType: "full"}
		}
		return backupTypeResult{BackupType: "differential", ParentRP: &lastFull}

	default:
		// Unknown chain type — fall back to full.
		return backupTypeResult{BackupType: "full"}
	}
}

// buildRestoreChain walks the parent_restore_point_id chain to build an ordered
// list of restore points from the base full backup to the given restore point.
// The returned slice is ordered oldest (full) first.
func (r *Runner) buildRestoreChain(rp db.RestorePoint) ([]db.RestorePoint, error) {
	chain := []db.RestorePoint{rp}
	current := rp
	for current.ParentRestorePointID > 0 {
		parent, err := r.db.GetRestorePoint(current.ParentRestorePointID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return nil, fmt.Errorf("missing parent restore point %d for restore point %d", current.ParentRestorePointID, current.ID)
			}
			return nil, fmt.Errorf("getting parent restore point %d: %w", current.ParentRestorePointID, err)
		}
		chain = append(chain, parent)
		current = parent
	}
	// Reverse to get oldest (full) first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

func protectedRestorePointIDs(all []db.RestorePoint, keepCount, keepDays int, now time.Time) map[int64]struct{} {
	protected := make(map[int64]struct{}, len(all))
	if len(all) == 0 {
		return protected
	}

	candidates := all
	if keepCount > 0 && keepCount < len(candidates) {
		candidates = candidates[:keepCount]
	}
	if keepDays > 0 {
		cutoff := now.AddDate(0, 0, -keepDays)
		filtered := make([]db.RestorePoint, 0, len(candidates))
		for _, rp := range candidates {
			if !rp.CreatedAt.Before(cutoff) {
				filtered = append(filtered, rp)
			}
		}
		candidates = filtered
	}

	byID := make(map[int64]db.RestorePoint, len(all))
	for _, rp := range all {
		byID[rp.ID] = rp
	}

	for _, rp := range candidates {
		current := rp
		for {
			if _, ok := protected[current.ID]; ok {
				break
			}
			protected[current.ID] = struct{}{}
			if current.ParentRestorePointID <= 0 {
				break
			}
			parent, ok := byID[current.ParentRestorePointID]
			if !ok {
				break
			}
			current = parent
		}
	}

	return protected
}

// hasContainerItems returns true if any item in the list is a container.
func hasContainerItems(items []db.JobItem) bool {
	for _, item := range items {
		if item.ItemType == "container" {
			return true
		}
	}
	return false
}

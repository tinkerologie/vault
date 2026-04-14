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
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/notify"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/tempdir"
	"github.com/ruaan-deysel/vault/internal/ws"
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

type restoreProgressReporter struct {
	JobID       int64
	RunID       int64
	ItemName    string
	ItemType    string
	ItemsDone   int
	ItemsFailed int
	ItemsTotal  int
}

// Runner executes backup and restore operations for jobs.
type Runner struct {
	db              *db.DB
	hub             *ws.Hub
	serverKey       []byte // AES-256 key for unsealing secrets.
	snapshotManager *db.SnapshotManager
	mu              sync.Mutex

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
}

// New creates a new Runner.
func New(database *db.DB, hub *ws.Hub, serverKey []byte) *Runner {
	return &Runner{
		db:        database,
		hub:       hub,
		serverKey: serverKey,
	}
}

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

// SetSnapshotManager sets the snapshot manager used to persist the database
// to the cache drive after successful backup jobs.
func (r *Runner) SetSnapshotManager(sm *db.SnapshotManager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshotManager = sm
}

// RunJob executes a backup for the given job ID. It is safe to call from
// a goroutine. It creates the job_run record, performs backups for each item,
// updates progress via WebSocket, and creates restore points on success.
func (r *Runner) RunJob(jobID int64) {
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

	// Resolve the actual backup type for this run (full/incremental/differential).
	btResult := r.resolveBackupType(job)

	run := db.JobRun{
		JobID:      job.ID,
		Status:     "running",
		BackupType: btResult.BackupType,
		ItemsTotal: len(items),
	}
	runID, err := r.db.CreateJobRun(run)
	if err != nil {
		log.Printf("runner: failed to create job run for job %d: %v", jobID, err)
		return
	}
	run.ID = runID

	// Create a cancellable context with a 4-hour timeout.
	// The cancel function is stored so CancelJob() can trigger it.
	const jobTimeout = 4 * time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
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
			if updateErr := r.db.UpdateJobRun(run); updateErr != nil {
				log.Printf("runner: failed to mark panicked run %d as failed: %v", runID, updateErr)
			}
			r.broadcast(map[string]any{
				"type":   "job_run_completed",
				"job_id": jobID,
				"run_id": runID,
				"status": "failed",
			})
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
	const stallWarnInterval = 30 * time.Minute
	const stallCancelTimeout = 2 * time.Hour
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.lastProgressMu.Lock()
				idle := time.Since(r.lastProgress)
				r.lastProgressMu.Unlock()

				if idle >= stallCancelTimeout {
					log.Printf("runner: job %d stalled for %v — cancelling", jobID, idle.Truncate(time.Minute))
					cancel()
					return
				}
				if idle >= stallWarnInterval {
					log.Printf("runner: WARNING job %d no progress for %v", jobID, idle.Truncate(time.Minute))
				}
			}
		}
	}()

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
		}

		if item.ItemType == "container" {
			backupItem.Settings["container_mode"] = job.ContainerMode
			if job.ContainerMode == "stop_all" {
				backupItem.Settings["no_stop"] = true
			}
			if ep, ok := settings["exclude_paths"]; ok {
				backupItem.Settings["exclude_paths"] = ep
			}
		}

		// VM items need the backup mode (snapshot or cold).
		if item.ItemType == "vm" {
			for key, value := range settings {
				backupItem.Settings[key] = value
			}
			backupItem.Settings["id"] = itemID
			backupItem.Settings["backup_mode"] = job.VMMode
		}

		if btResult.ParentRP != nil && (item.ItemType == "container" || item.ItemType == "vm" || item.ItemType == "folder") {
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

		result, checksums, backupErr := r.backupItem(ctx, backupItem, dest, itemPath, job.VerifyBackup, encryptPassphrase, job.Compression)
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
			itemResults = append(itemResults, resEntry)

			// Store checksums per item for restore-point metadata.
			if len(checksums) > 0 {
				itemChecksums[item.ItemName] = checksums
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

	status := "completed"
	if ctx.Err() != nil {
		status = "cancelled"
		if ctx.Err() == context.DeadlineExceeded {
			log.Printf("runner: job %d timed out after %v", jobID, time.Since(jobStart).Truncate(time.Second))
			run.Log = fmt.Sprintf("Job timed out after %v", time.Since(jobStart).Truncate(time.Second))
		} else {
			log.Printf("runner: job %d was cancelled after %v", jobID, time.Since(jobStart).Truncate(time.Second))
			run.Log = "Job was cancelled by user"
		}
	} else if itemsFailed > 0 && itemsDone == 0 {
		status = "failed"
	} else if itemsFailed > 0 {
		status = "partial"
	}

	run.Status = status
	run.Log = structuredDetails(itemResults)
	run.ItemsDone = itemsDone
	run.ItemsFailed = itemsFailed
	run.SizeBytes = totalSize
	if err := r.db.UpdateJobRun(run); err != nil {
		log.Printf("runner: failed to update job run %d: %v", runID, err)
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
		if _, err := r.db.CreateRestorePoint(rp); err != nil {
			log.Printf("runner: failed to create restore point for run %d: %v", runID, err)
		}

		// Write manifest.json to storage for out-of-band recovery.
		r.writeManifest(dest, basePath, job, runID, btResult.BackupType, itemsDone, itemsFailed, totalSize, itemChecksums, timestamp)

		// Auto-backup the SQLite database to storage.
		r.backupDatabase(dest, basePath)

		// Persist database to cache drive and USB backup after successful backup.
		// r.mu is already held by RunJob (line 289), so access snapshotManager
		// directly without re-locking.
		sm := r.snapshotManager
		if sm != nil {
			if err := sm.SaveSnapshotAndUSBBackup(); err != nil {
				log.Printf("runner: snapshot save error: %v", err)
			}
		}
	}

	if job.RetentionCount > 0 || job.RetentionDays > 0 {
		r.enforceRetention(dest, jobID, job.RetentionCount, job.RetentionDays)
	}

	// Execute post-script if configured.
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

	r.broadcast(map[string]any{
		"type":         "job_run_completed",
		"job_id":       jobID,
		"run_id":       runID,
		"status":       status,
		"items_done":   itemsDone,
		"items_failed": itemsFailed,
		"size_bytes":   totalSize,
	})

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

	// Send Unraid + Discord notifications based on job's notify_on setting.
	r.sendNotification(job, status, itemsDone, itemsFailed, totalSize, int(time.Since(jobStart).Seconds()), failedNames)
}

// backupItem executes a single item backup using the appropriate engine handler,
// writing output to a local temp dir and then to the storage adapter.
// If verify is true, it reads each file back and validates SHA-256 checksums.
// If passphrase is non-empty, each file is encrypted with age before uploading.
func (r *Runner) backupItem(ctx context.Context, item engine.BackupItem, dest db.StorageDestination, storagePath string, verify bool, passphrase string, compression string) (*engine.BackupResult, map[string]string, error) {
	stageOverride, _ := r.db.GetSetting("staging_dir_override", "")
	tmpDir, cleanup, err := tempdir.CreateBackupDir(tempdir.StorageConfig{Type: dest.Type, Config: dest.Config}, stageOverride)
	if err != nil {
		return nil, nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer cleanup()

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
		return nil, nil, fmt.Errorf("unknown item type: %s", item.Type)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("creating %s handler: %w", item.Type, err)
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
		return nil, nil, fmt.Errorf("backup %s: %w", item.Name, err)
	}

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return nil, nil, fmt.Errorf("creating storage adapter: %w", err)
	}
	defer storage.CloseAdapter(adapter)

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading backup dir: %w", err)
	}

	// Write files and compute SHA-256 checksums.
	checksums := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filePath := filepath.Join(tmpDir, entry.Name())
		f, err := os.Open(filePath) // #nosec G304 — tmpDir is a vault-controlled temp directory; entry.Name() from os.ReadDir
		if err != nil {
			return nil, nil, fmt.Errorf("opening backup file %s: %w", entry.Name(), err)
		}

		// Pipeline: file → (compress?) → (encrypt?) → tee(sha256) → storage.
		var reader io.Reader = f
		storageName := entry.Name()

		// Apply compression if configured.
		if compression != "" && compression != "none" {
			pr, pw := io.Pipe()
			cw, closeFn, ext, compErr := compressWriter(pw, compression)
			if compErr != nil {
				_ = f.Close()
				return nil, nil, fmt.Errorf("compressing %s: %w", entry.Name(), compErr)
			}
			storageName += ext
			go func() {
				_, copyErr := io.Copy(cw, f)
				closeErr := closeFn()
				if copyErr != nil {
					pw.CloseWithError(copyErr)
				} else if closeErr != nil {
					pw.CloseWithError(closeErr)
				} else {
					_ = pw.Close()
				}
			}()
			reader = pr
		}

		if passphrase != "" {
			encrypted, encErr := crypto.EncryptReader(passphrase, reader)
			if encErr != nil {
				_ = f.Close()
				return nil, nil, fmt.Errorf("encrypting %s: %w", entry.Name(), encErr)
			}
			reader = encrypted
			storageName += ".age"
		}

		hasher := sha256.New()
		tee := io.TeeReader(reader, hasher)

		destPath := filepath.Join(storagePath, storageName)
		if writeErr := adapter.Write(destPath, tee); writeErr != nil {
			_ = f.Close()
			return nil, nil, fmt.Errorf("writing %s to storage: %w", storageName, writeErr)
		}
		_ = f.Close()

		checksums[storageName] = hex.EncodeToString(hasher.Sum(nil))
	}

	// Verify: read files back from storage and re-compute SHA-256.
	if verify {
		for fileName, expectedHash := range checksums {
			destPath := filepath.Join(storagePath, fileName)
			reader, err := adapter.Read(destPath)
			if err != nil {
				return nil, nil, fmt.Errorf("verification read %s: %w", fileName, err)
			}
			verifyHasher := sha256.New()
			if _, err := io.Copy(verifyHasher, reader); err != nil {
				_ = reader.Close()
				return nil, nil, fmt.Errorf("verification hash %s: %w", fileName, err)
			}
			_ = reader.Close()
			actualHash := hex.EncodeToString(verifyHasher.Sum(nil))
			if actualHash != expectedHash {
				return nil, nil, fmt.Errorf("verification failed for %s: expected %s, got %s", fileName, expectedHash, actualHash)
			}
		}
	}

	return result, checksums, nil
}

// RestoreTarget describes a single item to restore.
type RestoreTarget struct {
	Name string
	Type string
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
		restoreErr := r.restoreItemWithReporter(restorePoint, t.Name, t.Type, destination, passphrase, reporter)
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
	return r.restoreItemWithReporter(restorePoint, itemName, itemType, destination, passphrase, restoreProgressReporter{})
}

func (r *Runner) restoreItemWithReporter(restorePoint db.RestorePoint, itemName, itemType, destination, passphrase string, reporter restoreProgressReporter) error {
	// For incremental/differential, walk the chain and restore in order.
	if restorePoint.BackupType == "incremental" || restorePoint.BackupType == "differential" {
		chain, err := r.buildRestoreChain(restorePoint)
		if err != nil {
			return fmt.Errorf("building restore chain: %w", err)
		}
		if usesMergedRestoreChain(itemType) {
			return r.restoreMergedChain(chain, itemName, itemType, destination, passphrase, reporter)
		}
		for i, rp := range chain {
			log.Printf("runner: restoring chain step %d/%d (type=%s, id=%d)",
				i+1, len(chain), rp.BackupType, rp.ID)
			if err := r.restoreSinglePoint(rp, itemName, itemType, destination, passphrase, reporter); err != nil {
				return fmt.Errorf("restoring chain step %d (id=%d): %w", i+1, rp.ID, err)
			}
		}
		return nil
	}
	return r.restoreSinglePoint(restorePoint, itemName, itemType, destination, passphrase, reporter)
}

func usesMergedRestoreChain(itemType string) bool {
	switch itemType {
	case "container", "vm":
		return true
	default:
		return false
	}
}

func (r *Runner) restoreMergedChain(chain []db.RestorePoint, itemName, itemType, destination, passphrase string, reporter restoreProgressReporter) error {
	stageOverride, _ := r.db.GetSetting("staging_dir_override", "")
	tmpDir, cleanup, err := tempdir.CreateRestoreDir(tempdir.StorageConfig{}, stageOverride)
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer cleanup()

	for i, rp := range chain {
		log.Printf("runner: staging chain step %d/%d (type=%s, id=%d)", i+1, len(chain), rp.BackupType, rp.ID)
		phaseStart := (i * 40) / len(chain)
		phaseEnd := ((i + 1) * 40) / len(chain)
		if err := r.stageRestorePointItem(rp, itemName, tmpDir, passphrase, phaseStart, phaseEnd, reporter); err != nil {
			return fmt.Errorf("staging chain step %d (id=%d): %w", i+1, rp.ID, err)
		}
	}

	return r.restoreStagedItem(chain[len(chain)-1].JobID, itemName, itemType, destination, tmpDir, reporter, 40, 100)
}

// restoreSinglePoint restores a single restore point (without chain logic).
func (r *Runner) restoreSinglePoint(restorePoint db.RestorePoint, itemName, itemType, destination, passphrase string, reporter restoreProgressReporter) error {
	stageOverride, _ := r.db.GetSetting("staging_dir_override", "")
	tmpDir, cleanup, err := tempdir.CreateRestoreDir(tempdir.StorageConfig{}, stageOverride)
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer cleanup()

	if err := r.stageRestorePointItem(restorePoint, itemName, tmpDir, passphrase, 0, 40, reporter); err != nil {
		return err
	}

	return r.restoreStagedItem(restorePoint.JobID, itemName, itemType, destination, tmpDir, reporter, 40, 100)
}

func (r *Runner) stageRestorePointItem(restorePoint db.RestorePoint, itemName, tmpDir, passphrase string, phaseStart, phaseEnd int, reporter restoreProgressReporter) error {
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

	var downloadedBytes int64

	for _, fi := range restoreFiles {
		reader, err := adapter.Read(fi.Path)
		if err != nil {
			return fmt.Errorf("reading %s from storage: %w", fi.Path, err)
		}

		// Compute SHA-256 on the raw storage content for checksum verification.
		storageHasher := sha256.New()
		var fileBytes int64
		hashingReader := io.TeeReader(&countingReader{
			reader: reader,
			onRead: func(n int64) {
				fileBytes += n
				if totalBytes > 0 {
					pct := scaleRestorePhaseProgress(phaseStart, phaseEnd, downloadedBytes+fileBytes, totalBytes)
					r.reportRestoreProgress(reporter, pct, fmt.Sprintf("Downloading %s", filepath.Base(fi.Path)))
				}
			},
		}, storageHasher)

		// Decrypt .age files if a passphrase is provided.
		dataReader := hashingReader
		localName := filepath.Base(fi.Path)
		if strings.HasSuffix(localName, ".age") && passphrase != "" {
			decrypted, decErr := crypto.DecryptReader(passphrase, hashingReader)
			if decErr != nil {
				_ = reader.Close()
				return fmt.Errorf("decrypting %s: %w", fi.Path, decErr)
			}
			dataReader = decrypted
			localName = strings.TrimSuffix(localName, ".age")
		}

		// Decompress the transport layer using the job's configured compression.
		decompressed, closeDecompress, restoredName, decmpErr := decompressStoredReader(dataReader, localName, job.Compression)
		if decmpErr != nil {
			_ = reader.Close()
			return fmt.Errorf("decompressing %s: %w", fi.Path, decmpErr)
		}
		dataReader = decompressed
		localName = restoredName

		localPath := filepath.Join(tmpDir, localName)
		out, err := os.Create(localPath) // #nosec G304 — tmpDir is vault-controlled; localName derived from storage entry name
		if err != nil {
			_ = reader.Close()
			return fmt.Errorf("creating local file %s: %w", localPath, err)
		}
		if _, copyErr := io.Copy(out, dataReader); copyErr != nil {
			_ = closeDecompress()
			_ = out.Close()
			_ = reader.Close()
			return fmt.Errorf("downloading %s: %w", fi.Path, copyErr)
		}
		_ = closeDecompress()
		_ = out.Close()
		_ = reader.Close()
		downloadedBytes += fileBytes
		if totalBytes > 0 {
			pct := scaleRestorePhaseProgress(phaseStart, phaseEnd, downloadedBytes, totalBytes)
			r.reportRestoreProgress(reporter, pct, fmt.Sprintf("Prepared %s", filepath.Base(localPath)))
		}

		// Verify checksum if available.
		storageName := filepath.Base(fi.Path)
		if expected, ok := expectedChecksums[storageName]; ok {
			actual := hex.EncodeToString(storageHasher.Sum(nil))
			if actual != expected {
				return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", storageName, expected, actual)
			}
		}
	}

	r.reportRestoreProgress(reporter, phaseEnd, "Restore data ready")

	return nil
}

func (r *Runner) restoreStagedItem(jobID int64, itemName, itemType, destination, tmpDir string, reporter restoreProgressReporter, phaseStart, phaseEnd int) error {
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

	restoreErr := handler.Restore(context.Background(), backupItem, tmpDir, progress)
	r.sendRestoreNotification(itemName, itemType, restoreErr)
	return restoreErr
}

// broadcast sends a JSON message to all connected WebSocket clients.
func (r *Runner) broadcast(data map[string]any) {
	msg, err := json.Marshal(data)
	if err != nil {
		log.Printf("runner: failed to marshal broadcast: %v", err)
		return
	}
	r.hub.Broadcast(msg)
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
			go func() {
				if err := notify.SendDiscord(webhookURL, embed); err != nil {
					log.Printf("runner: discord notification error: %v", err)
				}
			}()
		}
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

// writeManifest writes a manifest.json file to storage containing metadata
// about the backup run: files, checksums, encryption status, and timestamps.
// This enables out-of-band recovery without access to the database.
func (r *Runner) writeManifest(dest db.StorageDestination, basePath string, job db.Job, runID int64, backupType string, itemsDone, itemsFailed int, totalSize int64, itemChecksums map[string]map[string]string, timestamp string) {
	manifest := map[string]any{
		"version":      1,
		"job_name":     job.Name,
		"job_id":       job.ID,
		"run_id":       runID,
		"backup_type":  backupType,
		"encryption":   job.Encryption,
		"compression":  job.Compression,
		"items_done":   itemsDone,
		"items_failed": itemsFailed,
		"size_bytes":   totalSize,
		"verified":     job.VerifyBackup,
		"timestamp":    timestamp,
		"created_at":   time.Now().UTC().Format(time.RFC3339),
	}
	if len(itemChecksums) > 0 {
		manifest["checksums"] = itemChecksums
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

	manifestPath := filepath.Join(basePath, "manifest.json")
	if err := adapter.Write(manifestPath, strings.NewReader(string(data))); err != nil {
		log.Printf("runner: failed to write manifest to %s: %v", manifestPath, err)
	}
}

// backupDatabase copies the SQLite database file to storage alongside the
// backup data. This protects against database loss and enables disaster
// recovery from storage alone.
func (r *Runner) backupDatabase(dest db.StorageDestination, basePath string) {
	dbPath := r.db.Path()
	if dbPath == "" || dbPath == ":memory:" {
		return
	}

	f, err := os.Open(dbPath) // #nosec G304 — dbPath from r.db.Path(), set at daemon startup
	if err != nil {
		log.Printf("runner: failed to open database for backup: %v", err)
		return
	}
	defer f.Close()

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		log.Printf("runner: failed to create adapter for db backup: %v", err)
		return
	}
	defer storage.CloseAdapter(adapter)

	destPath := filepath.Join(basePath, "vault.db")
	if err := adapter.Write(destPath, f); err != nil {
		log.Printf("runner: failed to backup database to %s: %v", destPath, err)
	}
}

// enforceRetention deletes old restore points and their storage files.
// It handles both count-based and time-based (days) retention. Count-based
// retention runs first, then time-based cleanup removes any remaining
// restore points older than the specified days.
func (r *Runner) enforceRetention(dest db.StorageDestination, jobID int64, keepCount, keepDays int) {
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		log.Printf("runner: failed to create adapter for retention cleanup: %v", err)
	}
	defer storage.CloseAdapter(adapter)

	allRestorePoints, err := r.db.ListRestorePoints(jobID)
	if err != nil {
		log.Printf("runner: failed to list restore points for job %d: %v", jobID, err)
		return
	}

	protected := protectedRestorePointIDs(allRestorePoints, keepCount, keepDays, time.Now())
	for _, rp := range allRestorePoints {
		if _, ok := protected[rp.ID]; ok {
			continue
		}
		if adapter != nil && rp.StoragePath != "" {
			r.deleteStorageDir(adapter, rp.StoragePath)
		}
		if err := r.db.DeleteRestorePoint(rp.ID); err != nil {
			log.Printf("runner: failed to delete restore point %d for job %d: %v", rp.ID, jobID, err)
		}
	}
}

// deleteStorageDir recursively deletes all files under a storage path prefix.
func (r *Runner) deleteStorageDir(adapter storage.Adapter, prefix string) {
	r.DeleteStorageDir(adapter, prefix)
}

// DeleteStorageDir recursively deletes all files under a storage path prefix,
// then removes the directory itself.
func (r *Runner) DeleteStorageDir(adapter storage.Adapter, prefix string) {
	files, err := adapter.List(prefix)
	if err != nil {
		log.Printf("runner: failed to list storage dir %s for cleanup: %v", prefix, err)
		return
	}

	for _, fi := range files {
		if fi.IsDir {
			r.DeleteStorageDir(adapter, fi.Path)
			continue
		}
		if err := adapter.Delete(fi.Path); err != nil {
			log.Printf("runner: failed to delete storage file %s: %v", fi.Path, err)
		}
	}

	// Remove the now-empty directory itself.
	if err := adapter.Delete(prefix); err != nil {
		log.Printf("runner: failed to remove storage directory %s: %v", prefix, err)
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
			r.DeleteStorageDir(adapter, rp.StoragePath)
		}
	}

	// Also clean up the top-level job directory.
	jobDir := job.Name
	r.DeleteStorageDir(adapter, jobDir)

	return nil
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
				r.DeleteStorageDir(adapter, entry.Path)
			}
		}
	}
	return nil
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
			case strings.HasPrefix(fileName, "cube-") && strings.HasSuffix(fileName, ".zip"):
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
// referencing the original storage path.
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
		if backupType == "" {
			backupType = "full"
		}

		// Find or create the job.
		job, err := r.db.GetJobByName(jobName)
		if err != nil {
			// Job doesn't exist — create a minimal one.
			job = db.Job{
				Name:            jobName,
				Enabled:         false,
				BackupTypeChain: "full",
				RetentionCount:  7,
				RetentionDays:   30,
				Compression:     compression,
				Encryption:      encryption,
				ContainerMode:   "one_by_one",
				NotifyOn:        "failure",
				VerifyBackup:    true,
				StorageDestID:   storageDestID,
			}
			job.ID, err = r.db.CreateJob(job)
			if err != nil {
				log.Printf("runner: import: failed to create job %q: %v", jobName, err)
				continue
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
		itemsDone := 0
		if v, ok := b["items_done"].(float64); ok {
			itemsDone = int(v)
		}
		run := db.JobRun{
			JobID:      job.ID,
			Status:     "imported",
			BackupType: backupType,
			ItemsTotal: itemsDone,
			ItemsDone:  itemsDone,
		}
		runID, err := r.db.CreateJobRun(run)
		if err != nil {
			log.Printf("runner: import: failed to create job run for %q: %v", jobName, err)
			continue
		}

		// Build metadata JSON from manifest.
		metaBytes, _ := json.Marshal(b)

		rp := db.RestorePoint{
			JobRunID:    runID,
			JobID:       job.ID,
			BackupType:  backupType,
			StoragePath: storagePath,
			Metadata:    string(metaBytes),
			SizeBytes:   int64(sizeBytes),
		}
		if _, err := r.db.CreateRestorePoint(rp); err != nil {
			log.Printf("runner: import: failed to create restore point for %q: %v", jobName, err)
			continue
		}

		imported++
	}

	return imported, nil
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
			if err == sql.ErrNoRows {
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

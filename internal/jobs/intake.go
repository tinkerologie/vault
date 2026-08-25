// Package jobs implements Job Intake: the single module through which every
// Job write passes.
//
// Before this module existed, three callers each hand-rolled their own version
// of "create a Job": the REST handlers, the MCP tools, and the replication
// import path. They diverged. Jobs created over MCP were never handed to the
// scheduler and so silently never ran, skipped every enum, schedule, storage
// destination, folder-path and source/destination-overlap check, and — on
// delete — bypassed the guard that refuses to remove a Job while a restore for
// it is mid-write.
//
// The interface here is three methods. Everything a caller must know is that
// a Job write either succeeds completely or returns an error. "Completely"
// means validated, normalised, persisted, handed to the scheduler, flushed to
// the USB config, and broadcast to connected clients — in that order. There is
// no way to perform half of that sequence, because the sequence is not part of
// the interface.
package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/runner"
)

// ScheduleReloader reloads the cron scheduler so a changed Job takes effect
// without a daemon restart. The scheduler never reloads on its own.
type ScheduleReloader = func() error

// ConfigChangeHook flushes the database to USB flash after a mutation.
type ConfigChangeHook = func()

// PathValidator applies the browse handler's allowed-root policy to a
// caller-supplied folder path.
type PathValidator = func(string) error

// Runner is the slice of *runner.Runner that Job Intake needs. Declaring it
// here rather than taking the concrete type keeps the effects substitutable in
// tests without a live runner.
type Runner interface {
	Status() runner.RunStatus
	CancelJob(jobID int64) error
	CleanupJobStorageAsync(jobID int64, jobName string, dest db.StorageDestination, storagePaths []string)
	Broadcast(data map[string]any)
}

// Intake is the Job Intake module. Construct one per process with New and
// share it between every adapter — REST and MCP both hold a pointer to the
// same instance, which makes "both enforce the same policy" a structural fact
// rather than a convention a future caller can break.
type Intake struct {
	db           *db.DB
	runner       Runner
	reload       ScheduleReloader
	onChange     ConfigChangeHook
	validatePath PathValidator
}

// New builds the Job Intake module.
//
// reload, onChange and validatePath may each be nil, in which case that effect
// or check is skipped — the daemon supplies all three, but the CLI and tests
// construct an Intake with fewer. reload in particular is passed as a
// late-bound closure by the API server, because routes are built before the
// scheduler exists.
func New(database *db.DB, r Runner, reload ScheduleReloader, onChange ConfigChangeHook, validatePath PathValidator) *Intake {
	return &Intake{db: database, runner: r, reload: reload, onChange: onChange, validatePath: validatePath}
}

// SetPathValidator installs the folder allow-list policy after construction.
// The browse handler that owns the policy is built after the Intake is.
func (i *Intake) SetPathValidator(fn PathValidator) { i.validatePath = fn }

// SetConfigChangeHook installs the USB flush hook after construction.
func (i *Intake) SetConfigChangeHook(fn ConfigChangeHook) { i.onChange = fn }

// Create validates and persists a new Job together with its items, then runs
// the effect chain. The returned Job carries its assigned ID and the returned
// items carry theirs.
func (i *Intake) Create(job db.Job, items []db.JobItem) (db.Job, []db.JobItem, error) {
	if err := i.check(&job, items); err != nil {
		return db.Job{}, nil, err
	}
	id, err := i.db.CreateJob(job)
	if err != nil {
		return db.Job{}, nil, err
	}
	// The Job row and its items are one unit as far as a caller is concerned.
	// The repo has no transactional variant of these calls, so a failed item
	// insert is undone by removing the Job again (items cascade with it)
	// rather than leaving a half-built Job behind that would back up the wrong
	// set of things on its next run.
	for _, item := range items {
		item.JobID = id
		if _, addErr := i.db.AddJobItem(item); addErr != nil {
			if delErr := i.db.DeleteJob(id); delErr != nil {
				return db.Job{}, nil, fmt.Errorf("adding job item: %w (rollback also failed: %v)", addErr, delErr)
			}
			return db.Job{}, nil, addErr
		}
	}
	job.ID = id
	saved, err := i.db.GetJobItems(id)
	if err != nil {
		return db.Job{}, nil, err
	}
	i.applyEffects()
	return job, saved, nil
}

// Update replaces the Job at id. A nil items slice leaves the persisted items
// untouched; a non-nil slice (including an empty one) replaces them wholesale.
// Returns ErrJobNotFound if id does not exist — UpdateJob is an UPDATE … WHERE
// that does not error on zero rows, so without this check updating a missing
// id was a silent no-op.
func (i *Intake) Update(id int64, job db.Job, items []db.JobItem) (db.Job, []db.JobItem, error) {
	if _, err := i.db.GetJob(id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return db.Job{}, nil, ErrJobNotFound
		}
		return db.Job{}, nil, err
	}

	// A request that omits items leaves the persisted items in place, so check
	// the overlap against those rather than the (nil) request items —
	// otherwise a destination change alone could sneak the job into its own
	// source.
	// Read the persisted items once: they are both the overlap-check subject
	// when the request omits items, and the restore point if a wholesale
	// replacement fails halfway.
	existing, err := i.db.GetJobItems(id)
	if err != nil {
		return db.Job{}, nil, err
	}
	overlapItems := items
	if overlapItems == nil {
		overlapItems = existing
	}
	if err := i.checkJob(&job); err != nil {
		return db.Job{}, nil, err
	}
	if items != nil {
		if err := i.checkFolderPaths(items); err != nil {
			return db.Job{}, nil, err
		}
	}
	if err := i.checkOverlap(job.StorageDestID, overlapItems); err != nil {
		return db.Job{}, nil, err
	}

	job.ID = id
	if err := i.db.UpdateJob(job); err != nil {
		return db.Job{}, nil, err
	}
	if items != nil {
		// Replacing items is delete-then-insert, so a failure partway leaves
		// the Job with fewer items than either the old or the new set — the
		// one outcome the caller never asked for, and a silent way to stop
		// backing something up. Put the previous items back instead.
		if err := i.db.DeleteJobItems(id); err != nil {
			return db.Job{}, nil, err
		}
		for _, item := range items {
			item.JobID = id
			if _, addErr := i.db.AddJobItem(item); addErr != nil {
				return db.Job{}, nil, i.restoreItems(id, existing, addErr)
			}
		}
	}
	saved, err := i.db.GetJobItems(id)
	if err != nil {
		return db.Job{}, nil, err
	}
	i.applyEffects()
	return job, saved, nil
}

// restoreItems puts a Job's previous items back after a failed wholesale
// replacement, and reports the original failure. A rollback that itself fails
// is folded into the message: the Job is then genuinely inconsistent and the
// operator needs to see both halves.
func (i *Intake) restoreItems(id int64, previous []db.JobItem, cause error) error {
	if err := i.db.DeleteJobItems(id); err != nil {
		return fmt.Errorf("replacing job items: %w (rollback also failed: %v)", cause, err)
	}
	for _, item := range previous {
		item.JobID = id
		if _, err := i.db.AddJobItem(item); err != nil {
			return fmt.Errorf("replacing job items: %w (rollback also failed: %v)", cause, err)
		}
	}
	return cause
}

// DeleteOptions controls how far a Delete goes.
type DeleteOptions struct {
	// DeleteFiles also removes the Job's backup data from its storage
	// destination. The sweep runs asynchronously because a large backup on a
	// slow remote takes far longer than any caller will wait.
	DeleteFiles bool
}

// DeleteOutcome reports what the delete actually did, so an adapter can tell
// its caller whether work is still running in the background.
type DeleteOutcome struct {
	// CleanupStarted is true when a background storage sweep was launched.
	CleanupStarted bool
}

// Delete removes a Job and, optionally, its backup data.
//
// Returns ErrRestoreInProgress when a restore for this Job is running:
// interrupting one mid-write leaves half-restored data, and the delete would
// cascade-delete the restore's own records. A Job that is merely running (or
// queued) is cancelled first, best-effort — before this guard existed, delete
// removed the rows while the backup goroutine kept going, which is what made
// "delete" look like the only way to stop a job (issue #235).
func (i *Intake) Delete(id int64, opts DeleteOptions) (DeleteOutcome, error) {
	if i.runner != nil {
		if st := i.runner.Status(); st.Active && st.JobID == id && st.RunType == "restore" {
			return DeleteOutcome{}, ErrRestoreInProgress
		}
		_ = i.runner.CancelJob(id)
	}

	// When deleting backup files too, capture everything the cleanup needs
	// BEFORE removing the job row — the job and its restore points
	// cascade-delete, so they're gone once DeleteJob runs.
	var (
		cleanupJobName string
		cleanupDest    db.StorageDestination
		cleanupPaths   []string
		doCleanup      bool
	)
	if opts.DeleteFiles && i.runner != nil {
		job, jErr := i.db.GetJob(id)
		switch {
		case jErr == nil:
			dest, dErr := i.db.GetStorageDestination(job.StorageDestID)
			switch {
			case dErr == nil:
				rps, rErr := i.db.ListRestorePoints(id)
				if rErr != nil {
					// A real DB error here means we can't enumerate what to
					// clean; fail loudly rather than silently leaking files.
					return DeleteOutcome{}, rErr
				}
				for _, rp := range rps {
					if rp.StoragePath != "" {
						cleanupPaths = append(cleanupPaths, rp.StoragePath)
					}
				}
				cleanupJobName, cleanupDest, doCleanup = job.Name, dest, true
			case errors.Is(dErr, db.ErrNotFound):
				// Orphaned job (issue #113): no destination to clean. Proceed
				// with a record-only delete.
				log.Printf("job %d has no storage destination; deleting record only", id) // #nosec G706 //nolint:gosec // id is an int64 from the caller
			default:
				return DeleteOutcome{}, dErr
			}
		case errors.Is(jErr, db.ErrNotFound):
			// Job already gone; DeleteJob below is idempotent.
		default:
			return DeleteOutcome{}, jErr
		}
	}

	if err := i.db.DeleteJob(id); err != nil {
		return DeleteOutcome{}, err
	}
	if doCleanup {
		i.runner.CleanupJobStorageAsync(id, cleanupJobName, cleanupDest, cleanupPaths)
	}
	i.applyEffects()
	return DeleteOutcome{CleanupStarted: doCleanup}, nil
}

// check runs every create-time validation against a Job and its items.
func (i *Intake) check(job *db.Job, items []db.JobItem) error {
	if err := i.checkJob(job); err != nil {
		return err
	}
	if err := i.checkFolderPaths(items); err != nil {
		return err
	}
	return i.checkOverlap(job.StorageDestID, items)
}

// checkJob validates and normalises the Job's own fields, then confirms its
// storage destination exists.
func (i *Intake) checkJob(job *db.Job) error {
	if err := normalize(job); err != nil {
		return err
	}
	return i.checkStorageDest(job.StorageDestID)
}

// checkStorageDest rejects a storage_dest_id that does not exist. Without this
// the bad foreign key reached SQLite and surfaced as an opaque 500; the caller
// could not tell a typo from a server fault. A job may legitimately have no
// destination yet (0), which the runner treats as unconfigured.
func (i *Intake) checkStorageDest(destID int64) error {
	if destID == 0 {
		return nil
	}
	if _, err := i.db.GetStorageDestination(destID); err != nil {
		// Only a genuinely absent row is the caller's fault. Any other error
		// (DB closed, I/O failure) must stay a server fault — reporting it as
		// "destination not found" would send the operator hunting for a
		// config mistake during an outage.
		if errors.Is(err, db.ErrNotFound) {
			return invalid("storage_dest_id", "storage destination %d not found", destID)
		}
		return err
	}
	return nil
}

// checkFolderPaths applies the allowed-root policy to every folder item.
func (i *Intake) checkFolderPaths(items []db.JobItem) error {
	if i.validatePath == nil {
		return nil
	}
	for _, item := range items {
		if item.ItemType != "folder" {
			continue
		}
		path, err := folderSourcePath(item)
		if err != nil {
			return invalid("items", "folder item settings must be valid JSON")
		}
		if path == "" {
			continue
		}
		if err = i.validatePath(path); err != nil {
			return invalid("items", "folder path must be under /mnt, /boot, or a discovered ZFS mountpoint")
		}
	}
	return nil
}

// checkOverlap refuses a Job whose local destination sits inside (or contains)
// one of its own folder sources.
func (i *Intake) checkOverlap(storageDestID int64, items []db.JobItem) error {
	if reason, bad := folderSourceOverlap(i.localDestPath(storageDestID), items); bad {
		return invalid("", "%s", reason)
	}
	return nil
}

// localDestPath returns the on-array path of a local storage destination, or ""
// for remote destinations (sftp/smb/nfs/s3/webdav) which have no local path
// that could collide with a source tree.
func (i *Intake) localDestPath(storageDestID int64) string {
	if storageDestID == 0 {
		return ""
	}
	dest, err := i.db.GetStorageDestination(storageDestID)
	if err != nil || dest.Type != "local" {
		return ""
	}
	var cfg struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(dest.Config), &cfg) != nil {
		return ""
	}
	return cfg.Path
}

// applyEffects is the sequence every successful Job write must run: hand the
// change to the scheduler, flush it to USB, tell connected clients. Callers
// cannot skip a step or reorder them, because they never see them.
func (i *Intake) applyEffects() {
	if i.reload != nil {
		if err := i.reload(); err != nil {
			log.Printf("Warning: scheduler reload failed: %v", err)
		}
	}
	if i.onChange != nil {
		i.onChange()
	}
	if i.runner != nil {
		// A `config_changed` WebSocket event so dashboards / 3-2-1 compliance
		// widgets / recovery plans re-fetch derived state without a full page
		// reload.
		i.runner.Broadcast(map[string]any{
			"type":   "config_changed",
			"entity": "job",
		})
	}
}

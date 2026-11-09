// Package jobs implements the shared Job Intake policy for REST and MCP.
// Create and Update commit a Job and its items in one SQLite transaction.
// Scheduler reload, configuration flush scheduling and broadcast run afterward
// and have their own outcomes. Replication and import use separate paths.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/safepath"
)

// ScheduleReloader reloads the cron scheduler so a changed Job takes effect
// without a daemon restart. The scheduler never reloads on its own.
type ScheduleReloader = func() error

// ConfigChangeHook schedules a configuration flush after a mutation.
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

// SetConfigChangeHook installs the configuration flush hook after construction.
func (i *Intake) SetConfigChangeHook(fn ConfigChangeHook) { i.onChange = fn }

// Create validates and commits a new Job and its items, then runs the effects.
func (i *Intake) Create(job db.Job, items []db.JobItem) (db.Job, []db.JobItem, error) {
	if err := i.checkRequest(&job, items); err != nil {
		return db.Job{}, nil, err
	}
	saved, savedItems, err := i.db.CreateJobWithItems(context.Background(), job, items, checkMutation(job.StorageDestID, items))
	if err != nil {
		return db.Job{}, nil, err
	}
	i.applyEffects()
	return saved, savedItems, nil
}

// Update replaces the Job at id. Nil items preserve the saved selection and
// a non-nil slice replaces it. An empty slice clears the selection.
func (i *Intake) Update(id int64, job db.Job, items []db.JobItem) (db.Job, []db.JobItem, error) {
	if err := i.checkRequest(&job, items); err != nil {
		return db.Job{}, nil, err
	}
	saved, savedItems, err := i.db.UpdateJobWithItems(context.Background(), id, job, items, checkMutation(job.StorageDestID, items))
	if errors.Is(err, db.ErrNotFound) {
		return db.Job{}, nil, ErrJobNotFound
	}
	if err != nil {
		return db.Job{}, nil, err
	}
	i.applyEffects()
	return saved, savedItems, nil
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

// checkRequest validates fields and caller-supplied paths before taking the
// database write lock. The injected path validator can use the pool.
func (i *Intake) checkRequest(job *db.Job, items []db.JobItem) error {
	if err := normalize(job); err != nil {
		return err
	}
	return i.checkFolderPaths(items)
}

func checkMutation(destID int64, items []db.JobItem) func(db.JobMutationState) error {
	return func(state db.JobMutationState) error {
		// The persisted origin owns the destination of a replicated Job.
		if current := state.Current; current != nil && current.SourceID != 0 && destID != current.StorageDestID {
			return invalid("storage_dest_id", "the destination of a replicated job follows its replication source and cannot be changed")
		}
		if destID != 0 && !state.DestinationFound {
			return invalid("storage_dest_id", "storage destination %d not found", destID)
		}
		overlapItems := items
		if overlapItems == nil {
			overlapItems = state.Items
		}
		if reason, bad := folderSourceOverlap(localDestPath(state.Destination), overlapItems); bad {
			return invalid("", "%s", reason)
		}
		return nil
	}
}

// checkFolderPaths applies the allowed-root policy to every folder item.
func (i *Intake) checkFolderPaths(items []db.JobItem) error {
	for _, item := range items {
		if item.ItemType == "plugin" {
			// The name becomes a path component under the plugins directory.
			if _, err := safepath.NormalizeComponent(item.ItemName); err != nil {
				return invalid("items", "plugin item name must be a single folder name")
			}
			continue
		}
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
		if i.validatePath != nil {
			if err = i.validatePath(path); err != nil {
				return invalid("items", "folder path must be under /mnt, /boot, or a discovered ZFS mountpoint")
			}
		}
		// The allow-list above is lexical; this also follows symlinks.
		if _, err = safepath.CheckFolderLocation(path); err != nil {
			return invalid("items", "%s", err.Error())
		}
	}
	return nil
}

// localDestPath returns the configured local path, if any.
func localDestPath(dest db.StorageDestination) string {
	if dest.Type != "local" {
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

// applyEffects runs after persistence. Reload errors are logged and the later
// effects still run. The daemon's hook schedules a best-effort flush.
// Broadcast may block while queuing and does not guarantee client delivery.
// A panic stops the later effects without undoing the committed write.
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

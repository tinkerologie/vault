package runner

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"syscall"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
)

var (
	ErrReviewedRestoreChanged     = errors.New("reviewed Restore Request operation changed")
	ErrApprovedStorageUnavailable = errors.New("could not check the backup storage; try approval again when it is available")
	ErrRestorePassphrase          = errors.New("enter the correct Backup Passphrase for this Restore Point")
)

// ApprovedRestore carries one already claimed run. The review service supplies
// the persistence barrier and revalidation callback; the Runner owns execution.
type ApprovedRestore struct {
	RequestID  string
	Run        db.JobRun
	Plan       ApprovedRestorePlan
	Passphrase string
	Revalidate func(context.Context) error
	Flush      func() error
	checks     []string
	runContext context.Context
	cancelRun  context.CancelCauseFunc
}

// AdmitApprovedRestore reserves the idle Runner without adding a queue entry.
// prepare runs under that reservation and must durably link the run before
// returning it. A zero run ID means no work was claimed.
func (r *Runner) AdmitApprovedRestore(prepare func() (ApprovedRestore, error)) error {
	release, err := r.acquireUntrackedRestore()
	if err != nil {
		return err
	}
	transferred := false
	defer func() {
		if !transferred {
			release()
		}
	}()

	r.queueMu.Lock()
	queued := len(r.queue) != 0
	r.queueMu.Unlock()
	if queued {
		return ErrRunnerBusy
	}
	approved, err := prepare()
	if err != nil || approved.Run.ID == 0 {
		return err
	}

	// Publish the reservation before releasing the request/maintenance
	// boundary so DB replacement and Job deletion see this tracked restore.
	approved.runContext, approved.cancelRun = context.WithCancelCause(context.Background())
	r.publishRestoreCancellation(&RunStatus{
		Active: true, JobID: approved.Run.JobID, RunID: approved.Run.ID, JobName: approved.Plan.Job.Name,
		RunType: "restore", RestoreRequestID: approved.RequestID, ItemsTotal: 1, StartedAt: time.Now().Format(time.RFC3339),
	}, approved.cancelRun)
	transferred = true
	r.backgroundWork.Go(func() {
		defer release()
		plan := approved.Plan
		r.runTrackedRestore(approved.Run, plan.Point, []RestoreTarget{{Name: plan.Item.ItemName, Type: plan.Item.ItemType}},
			"", approved.Passphrase, plan.Job.Name, &approved)
	})
	return nil
}

func (r *Runner) finishTrackedRestore(run db.JobRun, approved *ApprovedRestore, requestStatus, reason string) bool {
	if err := r.persistTrackedRestore(run, approved, requestStatus, reason); err != nil {
		log.Printf("runner: could not record the terminal state of Restore Run %d: %v", run.ID, err)
		return false
	}
	if approved == nil {
		return true
	}
	if approved.Flush != nil {
		if err := approved.Flush(); err != nil {
			log.Printf("runner: could not save the terminal state of approved Restore Run %d to flash", run.ID)
		}
	}
	return true
}

func (r *Runner) persistTrackedRestore(run db.JobRun, approved *ApprovedRestore, requestStatus, reason string) error {
	// Serialize retirement and the terminal write with StopRestoreRun. A
	// stop that loses this race reads the committed outcome.
	r.cancelMu.Lock()
	defer r.cancelMu.Unlock()
	run.StopRequestedAt = r.restoreStopRequestedAt
	run.StoppedBy = r.restoreStoppedBy
	r.cancelFn = nil
	r.cancellingJobID = 0
	r.cancellingRunID = 0
	r.cancellingRestoreRequest = ""
	r.restoreStopRequestedAt = nil
	r.restoreStoppedBy = ""
	if approved == nil {
		return r.db.UpdateJobRun(run)
	}
	return r.db.FinishApprovedRestore(context.Background(), approved.RequestID, run, requestStatus, reason)
}

type approvedRestoreKey struct{}

func approvedRestoreContext(ctx context.Context) bool {
	approved, _ := ctx.Value(approvedRestoreKey{}).(bool)
	return approved
}

const (
	FolderDataStaged        = "required_backup_data_staged"
	FolderReviewRevalidated = "reviewed_operation_revalidated"
	FolderRestoreCompleted  = "folder_restore_completed"
)

func approvedRestoreFailure(err error) string {
	switch {
	case errors.Is(err, ErrReviewedRestoreChanged):
		return "reviewed_operation_changed"
	case errors.Is(err, engine.ErrRestoreNeedsApproval):
		return "manual_restore_required"
	case errors.Is(err, ErrRestorePassphrase):
		return "backup_passphrase_rejected"
	default:
		return "restore_failed"
	}
}

func changedApprovedRestore(err error) error {
	return fmt.Errorf("%w: %w", ErrReviewedRestoreChanged, err)
}

// approvedRestoreOperatorDiagnostic classifies trusted error types without
// copying storage URLs, paths, or credential-bearing provider messages.
func approvedRestoreOperatorDiagnostic(err error) string {
	switch {
	case errors.Is(err, engine.ErrRestoreArchiveEntryTooLarge):
		return "A backup archive file exceeds Vault's extraction size limit."
	case errors.Is(err, syscall.ENOSPC):
		return "A restore filesystem ran out of space."
	case errors.Is(err, fs.ErrPermission):
		return "A restore file could not be accessed because of its permissions."
	case errors.Is(err, fs.ErrNotExist):
		return "A required restore file is missing."
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, tar.ErrHeader):
		return "A required restore archive is incomplete or invalid."
	case errors.Is(err, ErrRestorePassphrase):
		return "The selected backup did not accept the Backup Passphrase."
	default:
		return ""
	}
}

func (approved *ApprovedRestore) revalidateStaged(ctx context.Context) error {
	approved.checks = append(approved.checks, FolderDataStaged)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := approved.Revalidate(ctx); err != nil {
		if ctx.Err() != nil && errors.Is(err, ErrApprovedStorageUnavailable) {
			return ctx.Err()
		}
		return err
	}
	approved.checks = append(approved.checks, FolderReviewRevalidated)
	return nil
}

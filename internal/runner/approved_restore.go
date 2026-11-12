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
	r.setRunStatus(&RunStatus{
		Active: true, JobID: approved.Run.JobID, RunID: approved.Run.ID, JobName: approved.Plan.Job.Name,
		RunType: "restore", RestoreRequestID: approved.RequestID, ItemsTotal: 1,
	})
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
	if approved == nil {
		_ = r.db.UpdateJobRun(run)
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.db.FinishApprovedRestore(ctx, approved.RequestID, run, requestStatus, reason); err != nil {
		log.Printf("runner: could not record the terminal state of approved Restore Run %d", run.ID)
		return false
	}
	if approved.Flush != nil {
		if err := approved.Flush(); err != nil {
			log.Printf("runner: could not save the terminal state of approved Restore Run %d to flash", run.ID)
		}
	}
	return true
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
	if err := approved.Revalidate(ctx); err != nil {
		return err
	}
	approved.checks = append(approved.checks, FolderReviewRevalidated)
	return nil
}

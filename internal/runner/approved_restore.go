package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/tinkerologie/vault/internal/db"
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
	Plan       FolderRestorePlan
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
		r.runTrackedRestore(approved.Run, plan.Point, []RestoreTarget{{Name: plan.ItemName, Type: "folder"}},
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
	case errors.Is(err, ErrRestorePassphrase):
		return "backup_passphrase_rejected"
	default:
		return "restore_failed"
	}
}

func changedFolderRestore(err error) error {
	return fmt.Errorf("%w: %v", ErrReviewedRestoreChanged, err)
}

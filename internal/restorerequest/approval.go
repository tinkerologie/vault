package restorerequest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/runner"
)

var (
	ErrApprovalPersistence = errors.New("the approval could not be saved to flash; no restore was started")
	ErrReviewDigest        = errors.New("the submitted approval does not match the reviewed operation")
	ErrRequestTerminal     = errors.New("this Restore Request cannot start")
)

func reviewDigest(operation json.RawMessage) string {
	sum := sha256.Sum256(operation)
	return hex.EncodeToString(sum[:])
}

// Approve runs only after the native operator proof has been verified. It
// accepts the stored operation verbatim and conditionally claims it under
// immediate Runner admission.
func (s *Service) Approve(ctx context.Context, id, digest, operator, passphrase string) (View, bool, error) {
	if !validRequestID(id) {
		return View{}, false, db.ErrNotFound
	}
	current, err := s.db.GetRestoreRequest(ctx, id, "")
	if err != nil {
		return View{}, false, err
	}
	if digest != reviewDigest(current.Operation) {
		return View{}, false, ErrReviewDigest
	}
	if current.Status != "pending" {
		return s.approvalOutcome(ctx, current, false)
	}

	if !s.maintenance.TryLock() {
		// The first approval may have committed while its response is
		// still in flight. Return that run on a later authenticated retry.
		return View{}, false, ErrBusy
	}
	defer s.maintenance.Unlock()

	var runID int64
	err = s.runner.AdmitApprovedRestore(func() (runner.ApprovedRestore, error) {
		var approved runner.ApprovedRestore
		now := s.now().UTC()
		if !current.ExpiresAt.After(now) {
			changed, err := s.db.ExpireRestoreRequests(ctx, now, id)
			s.flushChange(changed)
			return approved, err
		}

		var stored Operation
		if err := json.Unmarshal(current.Operation, &stored); err != nil {
			return approved, err
		}
		if !stored.Eligible {
			return approved, Invalid("the reviewed operation is ineligible for approval")
		}

		revalidate := func(ctx context.Context) (Operation, error) {
			fresh, err := Capture(ctx, s.db, stored.Input)
			var invalid Invalid
			if errors.Is(err, db.ErrNotFound) || errors.As(err, &invalid) {
				return Operation{}, runner.ErrReviewedRestoreChanged
			}
			if err != nil {
				return Operation{}, err
			}
			data, err := json.Marshal(fresh)
			if err != nil {
				return Operation{}, err
			}
			if !fresh.Eligible || reviewDigest(data) != digest {
				return Operation{}, runner.ErrReviewedRestoreChanged
			}
			return fresh, nil
		}

		fresh, err := revalidate(ctx)
		if err == nil {
			probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err = s.runner.CheckApprovedRestorePassphrase(probeCtx, fresh.plan, passphrase)
			cancel()
		}
		if errors.Is(err, runner.ErrReviewedRestoreChanged) {
			changed, err := s.db.InvalidateRestoreRequest(ctx, id, s.now().UTC())
			s.flushChange(changed)
			return approved, err
		}
		if err != nil {
			return approved, err
		}

		run := db.JobRun{JobID: fresh.plan.Job.ID, BackupType: fresh.plan.Point.BackupType, ItemsTotal: 1, RunType: "restore", Status: "running"}
		approval := db.RestoreApproval{RequestID: id, OperatorName: operator, ReviewDigest: digest}
		run.ID, err = s.db.ClaimRestoreApproval(ctx, approval, run, s.now)
		if err != nil || run.ID == 0 {
			return approved, err
		}
		if s.flush != nil {
			if err := s.flush(); err != nil {
				run.Status = "failed"
				run.Log = ErrApprovalPersistence.Error()
				// The local transaction may have reached flash despite an
				// error. Never revert this claim to pending.
				if finishErr := s.db.FinishApprovedRestore(context.Background(), id, run, "failed", "approval_persistence_failed"); finishErr != nil {
					log.Printf("restore requests: could not mark Restore Run %d failed after the persistence barrier", run.ID)
				}
				s.flushChange(true)
				return approved, ErrApprovalPersistence
			}
		}
		runID = run.ID
		return runner.ApprovedRestore{
			RequestID: id, Run: run, Plan: fresh.plan, Passphrase: passphrase, Flush: s.flush,
			Revalidate: func(ctx context.Context) error {
				_, err := revalidate(ctx)
				return err
			},
		}, nil
	})
	if err != nil {
		return View{}, false, err
	}

	current, err = s.db.GetRestoreRequest(ctx, id, "")
	if err != nil {
		return View{}, false, err
	}
	if current.Status == "pending" && !current.ExpiresAt.After(s.now()) {
		changed, err := s.db.ExpireRestoreRequests(ctx, s.now().UTC(), id)
		if err != nil {
			return View{}, false, err
		}
		s.flushChange(changed)
		current, err = s.db.GetRestoreRequest(ctx, id, "")
		if err != nil {
			return View{}, false, err
		}
	}
	return s.approvalOutcome(ctx, current, runID != 0)
}

func (s *Service) approvalOutcome(ctx context.Context, request db.RestoreRequest, started bool) (View, bool, error) {
	result, err := s.view(ctx, request, true, "")
	if err == nil && result.RunID == 0 {
		err = ErrRequestTerminal
	}
	return result, started, err
}

type ApprovalView struct {
	ApprovedAt   time.Time `json:"approved_at"`
	OperatorName string    `json:"operator_name,omitempty"`
}

type RunView struct {
	Status            string   `json:"status"`
	ItemsTotal        int      `json:"items_total"`
	ItemsDone         int      `json:"items_done"`
	ItemsFailed       int      `json:"items_failed"`
	Phase             string   `json:"phase,omitempty"`
	Percent           *int     `json:"percent,omitempty"`
	ApplicationHealth string   `json:"application_health"`
	ReadinessChecks   []string `json:"readiness_checks"`
}

func (s *Service) view(ctx context.Context, request db.RestoreRequest, operator bool, warning string) (View, error) {
	result, err := view(request, operator, warning)
	if err != nil || request.Status == "pending" || request.Status == "withdrawn" || request.Status == "expired" {
		return result, err
	}
	approval, err := s.db.GetRestoreApproval(ctx, request.ID)
	if errors.Is(err, db.ErrNotFound) {
		return result, nil
	}
	if err != nil {
		return View{}, err
	}
	result.RunID = approval.RunID
	if operator {
		result.Approval = &ApprovalView{ApprovedAt: approval.ApprovedAt, OperatorName: approval.OperatorName}
	}
	run, err := s.db.GetJobRun(approval.RunID)
	if errors.Is(err, db.ErrNotFound) {
		return result, nil // The Job was explicitly deleted; the audit link survives.
	}
	if err != nil {
		return View{}, err
	}
	runView := RunView{
		Status: run.Status, ItemsTotal: run.ItemsTotal, ItemsDone: run.ItemsDone, ItemsFailed: run.ItemsFailed,
		ApplicationHealth: "unchecked", ReadinessChecks: []string{},
	}
	if request.Status == "interrupted" {
		runView.Status = "interrupted"
	}
	var items []struct {
		ReadinessChecks []string `json:"readiness_checks"`
	}
	if json.Unmarshal([]byte(run.Log), &items) == nil {
		for _, item := range items {
			for _, check := range item.ReadinessChecks {
				switch check {
				case runner.FolderDataStaged, runner.FolderReviewRevalidated, runner.FolderRestoreCompleted:
					runView.ReadinessChecks = append(runView.ReadinessChecks, check)
				}
			}
		}
	}
	status := s.runner.Status()
	if status.Active && status.RunID == run.ID && run.Status == "running" {
		runView.Phase = status.Phase
		if status.CurrentItem != "" {
			runView.Percent = &status.CurrentItemPercent
		}
	}
	result.Run = &runView
	return result, nil
}

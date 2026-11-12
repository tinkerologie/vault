package runner

import (
	"context"
	"errors"
	"strings"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/storage"
)

// ApprovedRestoreWork executes the reviewed workload through the tracked Runner.
type ApprovedRestoreWork interface {
	RequiresPassphrase() bool
	restore(context.Context, *Runner, *ApprovedRestore, string, restoreProgressReporter) error
}

type ApprovedRestorePlan struct {
	Job         db.Job
	Destination db.StorageDestination
	Point       db.RestorePoint
	Steps       []ApprovedRestoreStep
	Item        ItemSelection
	Work        ApprovedRestoreWork
}

type ApprovedRestoreStep struct {
	Point   db.RestorePoint
	Objects []storage.FileInfo
}

func (plan ApprovedRestorePlan) RequiresPassphrase() bool {
	if plan.Work != nil && plan.Work.RequiresPassphrase() {
		return true
	}
	for _, step := range plan.Steps {
		for _, object := range step.Objects {
			if strings.HasSuffix(object.Path, ".age") {
				return true
			}
		}
	}
	return false
}

var approvedReadSlots = make(chan struct{}, 2)

// CheckApprovedRestorePassphrase authenticates access to the selected Backup Chain
// before the Runner claims the Restore Request.
func (r *Runner) CheckApprovedRestorePassphrase(ctx context.Context, plan ApprovedRestorePlan, passphrase string) error {
	var chunked bool
	for _, step := range plan.Steps {
		_, isChunked, err := lookupItemManifest(step.Point, plan.Item.ItemName)
		if err != nil {
			return changedApprovedRestore(err)
		}
		chunked = chunked || isChunked
	}
	if !plan.RequiresPassphrase() && !chunked {
		return nil
	}
	if plan.RequiresPassphrase() && passphrase == "" {
		return ErrRestorePassphrase
	}
	select {
	case approvedReadSlots <- struct{}{}:
	default:
		return ErrRunnerBusy
	}
	// A legacy storage adapter can remain blocked after cancellation. The
	// worker keeps its slot until it actually returns.
	result := make(chan error, 1)
	go func() {
		defer func() { <-approvedReadSlots }()
		defer func() {
			if recover() != nil {
				result <- ErrApprovedStorageUnavailable
			}
		}()
		result <- r.probeApprovedRestorePassphrase(ctx, plan, passphrase, chunked)
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ErrApprovedStorageUnavailable
	}
}

func (r *Runner) probeApprovedRestorePassphrase(ctx context.Context, plan ApprovedRestorePlan, passphrase string, chunked bool) error {
	adapter, err := storage.NewAdapterWithOptions(plan.Destination.Type, plan.Destination.Config, storage.Options{MetadataProbeContext: ctx})
	if err != nil {
		return ErrApprovedStorageUnavailable
	}
	defer storage.CloseAdapter(adapter)
	stopOnCancel := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stopOnCancel()
	if chunked {
		_, err := OpenDedupRepoWithFallback(r.db, adapter, plan.Destination.ID, r.serverKey, passphrase)
		if errors.Is(err, dedup.ErrPassphraseMismatch) || (passphrase == "" && errors.Is(err, dedup.ErrServerKeyMismatch)) {
			return ErrRestorePassphrase
		}
		if err != nil {
			return ErrApprovedStorageUnavailable
		}
	}
	for _, step := range plan.Steps {
		for _, object := range step.Objects {
			if ctx.Err() != nil {
				return ErrApprovedStorageUnavailable
			}
			if !strings.HasSuffix(object.Path, ".age") {
				continue
			}
			if err := checkRestoreObjectPassphrase(adapter, object, passphrase); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkRestoreObjectPassphrase(adapter storage.Adapter, object storage.FileInfo, passphrase string) error {
	source, err := adapter.ReadRange(object.Path, 0, min(object.Size, 16<<10))
	if err != nil {
		if storage.IsNotExist(err) {
			return ErrReviewedRestoreChanged
		}
		return ErrApprovedStorageUnavailable
	}
	defer source.Close()
	plain, err := crypto.DecryptReader(passphrase, source)
	if crypto.IsWrongPassphrase(err) {
		return ErrRestorePassphrase
	}
	if err != nil {
		return changedApprovedRestore(err)
	}
	return plain.Close()
}

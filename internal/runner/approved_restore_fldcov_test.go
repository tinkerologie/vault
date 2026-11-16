package runner

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/storage"
)

func TestApprovedRestoreFailureClassification(t *testing.T) {
	for err, want := range map[error]string{
		changedApprovedRestore(errors.New("x")):             "reviewed_operation_changed",
		fmt.Errorf("w: %w", engine.ErrRestoreNeedsApproval): "manual_restore_required",
		ErrRestorePassphrase:                                "backup_passphrase_rejected",
		errors.New("fixture private provider detail"):       "restore_failed",
	} {
		if got := approvedRestoreFailure(err); got != want {
			t.Fatalf("%v: got %q want %q", err, got, want)
		}
	}
	for err, want := range map[error]string{
		engine.ErrRestoreArchiveEntryTooLarge: "extraction size limit",
		syscall.ENOSPC:                        "ran out of space",
		fs.ErrPermission:                      "permissions",
		fs.ErrNotExist:                        "missing",
		io.ErrUnexpectedEOF:                   "incomplete or invalid",
		tar.ErrHeader:                         "incomplete or invalid",
		ErrRestorePassphrase:                  "Backup Passphrase",
	} {
		if got := approvedRestoreOperatorDiagnostic(fmt.Errorf("/private/fixture/path: %w", err)); !strings.Contains(got, want) || strings.Contains(got, "/private") {
			t.Fatalf("%v: diagnostic %q", err, got)
		}
	}
	if got := approvedRestoreOperatorDiagnostic(errors.New("s3://fixture-bucket secret")); got != "" {
		t.Fatalf("untrusted error produced diagnostic %q", got)
	}
	if approvedRestoreContext(t.Context()) || !approvedRestoreContext(context.WithValue(t.Context(), approvedRestoreKey{}, true)) {
		t.Fatal("approved restore context marker is wrong")
	}
}

func TestRevalidateStagedPrefersCancellation(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	approved := &ApprovedRestore{Revalidate: func(context.Context) error { called = true; return nil }}
	if err := approved.revalidateStaged(cancelled); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled before revalidation: %v called=%v", err, called)
	}

	ctx, cancelLater := context.WithCancel(t.Context())
	approved = &ApprovedRestore{Revalidate: func(context.Context) error {
		cancelLater()
		return ErrApprovedStorageUnavailable
	}}
	if err := approved.revalidateStaged(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("storage error during cancellation: %v", err)
	}
	if strings.Join(approved.checks, ",") != FolderDataStaged {
		t.Fatalf("checks: %v", approved.checks)
	}
}

func TestAdmitApprovedRestoreRefusesWhileJobsAreQueued(t *testing.T) {
	f := newFldcovFixture(t)
	f.r.queueMu.Lock()
	f.r.queue = append(f.r.queue, QueueEntry{JobID: f.job.ID})
	f.r.queueMu.Unlock()
	prepared := false
	err := f.r.AdmitApprovedRestore(func() (ApprovedRestore, error) { prepared = true; return ApprovedRestore{}, nil })
	if !errors.Is(err, ErrRunnerBusy) || prepared {
		t.Fatalf("queued Runner admitted approval: %v prepared=%v", err, prepared)
	}
}

type passphraseWork struct{}

func (passphraseWork) RequiresPassphrase() bool { return true }

func (passphraseWork) restore(context.Context, *Runner, *ApprovedRestore, string, restoreProgressReporter) error {
	return nil
}

// fldcovErrAdapter fails metadata reads with a provider error.
type fldcovErrAdapter struct{ storage.Adapter }

func (fldcovErrAdapter) ReadRange(string, int64, int64) (io.ReadCloser, error) {
	return nil, errors.New("fixture provider unavailable")
}

func TestApprovedPassphraseProbeClassifiesStorageEvidence(t *testing.T) {
	f := newFldcovFixture(t)
	point := f.point(t, "full", "probe/full", 0, "")
	encrypted := storage.FileInfo{Path: "probe/full/archive/data.tar.age", Size: 64}
	plan := func(objects ...storage.FileInfo) ApprovedRestorePlan {
		return ApprovedRestorePlan{
			Job: f.job, Destination: f.dest, Point: point, Item: ItemSelection{ItemName: "archive", ItemType: "folder"},
			Steps: []ApprovedRestoreStep{{Point: point, Objects: objects}},
		}
	}

	if !(ApprovedRestorePlan{Work: passphraseWork{}}).RequiresPassphrase() {
		t.Fatal("work that needs a passphrase was not reported")
	}
	if got := approvedRestorePoints(plan()); len(got) != 1 || got[0].ID != point.ID {
		t.Fatalf("plan points: %+v", got)
	}

	bad := plan()
	bad.Steps[0].Point.Metadata = `{"item_manifests":{"archive":"zz"}}`
	if err := f.r.CheckApprovedRestorePassphrase(t.Context(), bad, ""); !errors.Is(err, ErrReviewedRestoreChanged) {
		t.Fatalf("invalid manifest: %v", err)
	}
	if err := f.r.CheckApprovedRestorePassphrase(t.Context(), plan(encrypted), ""); !errors.Is(err, ErrRestorePassphrase) {
		t.Fatalf("missing passphrase: %v", err)
	}
	if err := f.r.CheckApprovedRestorePassphrase(t.Context(), plan(encrypted), "secret"); !errors.Is(err, ErrReviewedRestoreChanged) {
		t.Fatalf("missing encrypted object: %v", err)
	}

	dir := filepath.Join(f.root, "probe/full/archive")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.tar.age"), []byte("not an age file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.r.CheckApprovedRestorePassphrase(t.Context(), plan(encrypted), "secret"); !errors.Is(err, ErrReviewedRestoreChanged) {
		t.Fatalf("corrupt encrypted object: %v", err)
	}
	if err := checkRestoreObjectPassphrase(fldcovErrAdapter{}, encrypted, "secret"); !errors.Is(err, ErrApprovedStorageUnavailable) {
		t.Fatalf("provider error: %v", err)
	}

	unknownStorage := plan(encrypted)
	unknownStorage.Destination.Type = "fixture-unknown-storage"
	if err := f.r.CheckApprovedRestorePassphrase(t.Context(), unknownStorage, "secret"); !errors.Is(err, ErrApprovedStorageUnavailable) {
		t.Fatalf("unknown storage: %v", err)
	}

	chunked := plan()
	chunked.Steps[0].Point.Metadata = `{"item_manifests":{"archive":"` + strings.Repeat("ab", 32) + `"}}`
	if err := f.r.CheckApprovedRestorePassphrase(t.Context(), chunked, ""); !errors.Is(err, ErrApprovedStorageUnavailable) {
		t.Fatalf("uninitialized dedup repository: %v", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.r.probeApprovedRestorePassphrase(cancelled, plan(encrypted), "secret", false); !errors.Is(err, ErrApprovedStorageUnavailable) {
		t.Fatalf("cancelled probe: %v", err)
	}
	if err := f.r.CheckApprovedRestorePassphrase(cancelled, plan(encrypted), "secret"); !errors.Is(err, ErrApprovedStorageUnavailable) {
		t.Fatalf("cancelled check: %v", err)
	}

	for range cap(approvedReadSlots) {
		approvedReadSlots <- struct{}{}
	}
	defer func() {
		for range cap(approvedReadSlots) {
			<-approvedReadSlots
		}
	}()
	if err := f.r.CheckApprovedRestorePassphrase(t.Context(), plan(encrypted), "secret"); !errors.Is(err, ErrRunnerBusy) {
		t.Fatalf("saturated probe slots: %v", err)
	}
}

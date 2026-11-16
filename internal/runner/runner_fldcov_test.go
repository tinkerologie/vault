package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

func TestLookupItemManifestRejectsAmbiguousRowLevelIDs(t *testing.T) {
	rowID := make([]byte, 32)
	for name, point := range map[string]db.RestorePoint{
		"unreadable metadata":   {Metadata: "{not json", ManifestID: rowID},
		"multi-item membership": {Metadata: `{"item_sizes":{"archive":1,"other":2}}`, ManifestID: rowID},
		"other item membership": {Metadata: `{"item_sizes":{"other":2}}`, ManifestID: rowID},
	} {
		if _, _, err := lookupItemManifest(point, "archive"); !errors.Is(err, errInvalidItemManifest) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, chunked, err := lookupItemManifest(db.RestorePoint{Metadata: "{not json"}, "archive"); err != nil || chunked {
		t.Fatalf("unreadable classic metadata: %v %v", chunked, err)
	}
}

func TestRestoreChainRefusesInvalidManifestMetadata(t *testing.T) {
	f := newFldcovFixture(t)
	const invalid = `{"item_manifests":{"archive":"short"}}`
	bad := f.point(t, "full", "invalid/full", 0, invalid)
	if err := f.r.restoreItemChain(t.Context(), bad, "archive", "folder", "", "", nil, false, restoreProgressReporter{}); !errors.Is(err, errInvalidItemManifest) {
		t.Fatalf("selected point: %v", err)
	}
	if err := f.r.restoreSinglePoint(t.Context(), bad, "archive", "folder", "", "", nil, false, restoreProgressReporter{}); !errors.Is(err, errInvalidItemManifest) {
		t.Fatalf("single point: %v", err)
	}

	orphan := f.point(t, "incremental", "orphan/incr", 999999, "")
	if err := f.r.restoreItemChain(t.Context(), orphan, "archive", "folder", "", "", nil, false, restoreProgressReporter{}); err == nil ||
		!strings.Contains(err.Error(), "building restore chain") {
		t.Fatalf("broken chain: %v", err)
	}

	child := f.point(t, "incremental", "invalid/incr", bad.ID, `{"item_sizes":{"archive":1}}`)
	err := f.r.restoreItemChain(t.Context(), child, "archive", "folder", "", "", nil, false, restoreProgressReporter{})
	if !errors.Is(err, errInvalidItemManifest) || !strings.Contains(err.Error(), fmt.Sprintf("restore point %d", bad.ID)) {
		t.Fatalf("invalid earlier step: %v", err)
	}
}

type panickingRestoreWork struct{}

func (panickingRestoreWork) RequiresPassphrase() bool { return false }

func (panickingRestoreWork) restore(context.Context, *Runner, *ApprovedRestore, string, restoreProgressReporter) error {
	panic("fixture private panic detail")
}

func TestApprovedRestorePanicFailsRunWithoutLeakingDetail(t *testing.T) {
	f := newFldcovFixture(t)
	point := f.point(t, "full", "panic/full", 0, "")
	now := time.Now()
	request := db.RestoreRequest{
		ID: strings.Repeat("c", 32), OwnerDigest: "fldcov", RequesterName: "fixture",
		Operation: json.RawMessage(`{"input":"fldcov"}`), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if _, created, err := f.db.AdmitRestoreRequest(t.Context(), request, "x", "x", 5, 5, now.Add(-time.Hour)); err != nil || !created {
		t.Fatalf("admit request: %v %v", created, err)
	}
	var flushed int
	err := f.r.AdmitApprovedRestore(func() (ApprovedRestore, error) {
		run := db.JobRun{JobID: f.job.ID, BackupType: "full", ItemsTotal: 1, RunType: "restore", Status: "running"}
		id, err := f.db.ClaimRestoreApproval(t.Context(), db.RestoreApproval{RequestID: request.ID, OperatorName: "root", ReviewDigest: "d"}, run, time.Now)
		if err != nil {
			return ApprovedRestore{}, err
		}
		run.ID = id
		return ApprovedRestore{
			RequestID: request.ID, Run: run,
			Plan: ApprovedRestorePlan{
				Job: f.job, Destination: f.dest, Point: point,
				Item: ItemSelection{ItemName: "archive", ItemType: "folder"}, Work: panickingRestoreWork{},
			},
			Flush:      func() error { flushed++; return errors.New("fixture flush failure") },
			Revalidate: func(context.Context) error { return nil },
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.r.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	stored, err := f.db.GetRestoreRequest(t.Context(), request.ID, "")
	if err != nil || stored.Status != "failed" || stored.TerminalReason != "restore_failed" {
		t.Fatalf("request after panic: %+v %v", stored, err)
	}
	approval, err := f.db.GetRestoreApproval(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.db.GetJobRun(approval.RunID)
	if err != nil || run.Status != "failed" || run.Log != "Internal error during approved restore" {
		t.Fatalf("run after panic: %+v %v", run, err)
	}
	if strings.Contains(run.Log, "fixture private panic detail") || flushed != 1 {
		t.Fatalf("panic outcome leaked detail or skipped flush: %q flushed=%d", run.Log, flushed)
	}
	if f.r.Status().Active {
		t.Fatal("runner still reports the panicked restore as active")
	}
}

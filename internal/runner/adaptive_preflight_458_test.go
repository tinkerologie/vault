package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/storage"
)

func TestAdaptiveSettingIntInvalidFallsBackA458(t *testing.T) {
	r, d := a458Runner(t)
	a458SetSetting(t, d, "adaptive_recheck_minutes", "not-a-number")
	if got := r.adaptiveSettingInt("adaptive_recheck_minutes"); got <= 0 {
		t.Fatalf("got %d, want the positive catalog default", got)
	}
}

func TestAdaptiveBusyReasonSkipsUnprobeableItemsA458(t *testing.T) {
	r, _ := a458Runner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	items := []db.JobItem{
		{ItemType: "container", ItemName: "a458-no-such-container"},
		{ItemType: "vm", ItemName: "a458-no-such-vm"},
		{ItemType: "folder", ItemName: "bad-json", Settings: "{"},
		{ItemType: "folder", ItemName: "no-path", Settings: `{"path":""}`},
		{ItemType: "plugin", ItemName: "unsupported"},
	}
	// Probes that cannot run, or find nothing, are fail-open (idle).
	if reason := r.adaptiveBusyReason(ctx, items); reason != "" {
		t.Fatalf("got busy reason %q, want idle", reason)
	}
}

func TestAdaptivePostponeMaxWindowElapsedA458(t *testing.T) {
	r, d := a458Runner(t)
	a458SetSetting(t, d, "adaptive_max_postpone_minutes", "0")
	if r.adaptivePostpone(1, "job", "busy") {
		t.Fatal("a zero max-postpone window must run immediately")
	}
	r.postponeMu.Lock()
	_, open := r.postponedSince[1]
	r.postponeMu.Unlock()
	if open {
		t.Fatal("postpone window should be cleared")
	}
}

// TestAdaptivePostponeRecheckA458 drives the armed re-check timer with fake
// time: a disabled job clears its window, an enabled one re-runs.
func TestAdaptivePostponeRecheckA458(t *testing.T) {
	r, d := a458Runner(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := a458LocalDest(t, d, "adaptive", t.TempDir())
	disabledID, err := d.CreateJob(db.Job{Name: "a458-disabled", StorageDestID: dest.ID, BackupTypeChain: "full", Enabled: false, AdaptiveEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	enabledID, err := d.CreateJob(db.Job{Name: "a458-enabled", StorageDestID: dest.ID, BackupTypeChain: "full", Enabled: true, AdaptiveEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"path": src})
	if _, err := d.AddJobItem(db.JobItem{JobID: enabledID, ItemType: "folder", ItemName: "src", Settings: string(settings)}); err != nil {
		t.Fatal(err)
	}
	// The folder is idle immediately, so the re-check run backs up.
	a458SetSetting(t, d, "adaptive_folder_idle_minutes", "0")

	synctest.Test(t, func(t *testing.T) {
		if !r.adaptivePostpone(disabledID, "a458-disabled", "busy") {
			t.Fatal("first postpone should defer")
		}
		// Second call in the same window: no new row, no second timer.
		if !r.adaptivePostpone(disabledID, "a458-disabled", "busy") {
			t.Fatal("second postpone should defer")
		}
		if !r.adaptivePostpone(enabledID, "a458-enabled", "busy") {
			t.Fatal("enabled job postpone should defer")
		}
		time.Sleep(24 * time.Hour)
		synctest.Wait()
	})

	r.postponeMu.Lock()
	_, disabledOpen := r.postponedSince[disabledID]
	_, enabledOpen := r.postponedSince[enabledID]
	r.postponeMu.Unlock()
	if disabledOpen || enabledOpen {
		t.Fatalf("windows still open: disabled=%v enabled=%v", disabledOpen, enabledOpen)
	}
	runs, err := d.GetJobRuns(disabledID, 10)
	if err != nil || len(runs) != 1 || runs[0].Status != "postponed" {
		t.Fatalf("disabled job runs %+v (%v), want exactly one postponed row", runs, err)
	}
	runs, err = d.GetJobRuns(enabledID, 10)
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	for _, run := range runs {
		completed = completed || run.Status == "completed"
	}
	if !completed {
		t.Fatalf("enabled job should have re-run to completion, got %+v", runs)
	}
}

func TestAdaptivePostponeClosedDBA458(t *testing.T) {
	r, d := a458Runner(t)
	_ = d.Close()
	synctest.Test(t, func(t *testing.T) {
		// Settings fall back to defaults; the postponed-row insert fails
		// and is logged; the re-check then finds no job and clears.
		if !r.adaptivePostpone(5, "gone", "busy") {
			t.Fatal("should defer")
		}
		time.Sleep(24 * time.Hour)
		synctest.Wait()
	})
}

func TestFreeSpaceAtRealA458(t *testing.T) {
	if free, err := freeSpaceAt(t.TempDir()); err != nil || free <= 0 {
		t.Fatalf("freeSpaceAt(tempdir) = %d, %v", free, err)
	}
	if _, err := freeSpaceAt(filepath.Join(t.TempDir(), "missing", "x")); err == nil {
		t.Fatal("freeSpaceAt(missing) should fail")
	}
}

func TestPreflightRestoreReachabilityFailuresA458(t *testing.T) {
	r, d := a458Runner(t)
	stubFreeSpace(t, 1<<40)

	if res := r.PreflightRestore(db.Job{StorageDestID: 4242}, db.RestorePoint{}, "", ""); res.OK {
		t.Fatal("missing destination must block")
	}

	bogusID, err := d.CreateStorageDestination(db.StorageDestination{Name: "bogus", Type: "bogus", Config: "{}"})
	if err == nil {
		if res := r.PreflightRestore(db.Job{StorageDestID: bogusID}, db.RestorePoint{}, "", ""); res.OK {
			t.Fatal("unknown adapter type must block")
		}
	}

	missing := a458LocalDest(t, d, "missing-root", filepath.Join(t.TempDir(), "does-not-exist"))
	res := r.PreflightRestore(db.Job{StorageDestID: missing.ID}, db.RestorePoint{StoragePath: "x"}, "", "")
	if c := checkByID(res, "reachable"); c.Status != "fail" {
		t.Fatalf("reachable = %+v, want fail", c)
	}
}

func TestCheckRestorePointPresentBranchesA458(t *testing.T) {
	root := t.TempDir()
	local := storage.NewLocalAdapter(root)
	dedupDest := db.StorageDestination{DedupEnabled: true}

	if c := checkRestorePointPresent(local, dedupDest, db.RestorePoint{}); c.Status != "fail" {
		t.Fatalf("dedup missing repo: %+v", c)
	}
	if err := os.MkdirAll(filepath.Join(root, dedup.RepoRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, dedup.RepoRoot, "repo.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := checkRestorePointPresent(local, dedupDest, db.RestorePoint{}); c.Status != "ok" {
		t.Fatalf("dedup repo present: %+v", c)
	}
	statErr := &a458Adapter{statFn: func(string) (storage.FileInfo, error) { return storage.FileInfo{}, errors.New("io") }}
	if c := checkRestorePointPresent(statErr, dedupDest, db.RestorePoint{}); c.Status != "warn" {
		t.Fatalf("dedup stat error: %+v", c)
	}

	plain := db.StorageDestination{}
	if c := checkRestorePointPresent(local, plain, db.RestorePoint{}); c.Status != "skip" {
		t.Fatalf("no storage path: %+v", c)
	}
	listErr := &a458Adapter{listFn: func(string) ([]storage.FileInfo, error) { return nil, errors.New("io") }}
	if c := checkRestorePointPresent(listErr, plain, db.RestorePoint{StoragePath: "rp"}); c.Status != "warn" {
		t.Fatalf("list error: %+v", c)
	}
	notFound := &a458Adapter{listFn: func(string) ([]storage.FileInfo, error) { return nil, fs.ErrNotExist }}
	if c := checkRestorePointPresent(notFound, plain, db.RestorePoint{StoragePath: "rp"}); c.Status != "fail" {
		t.Fatalf("not found: %+v", c)
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if c := checkRestorePointPresent(local, plain, db.RestorePoint{StoragePath: "empty"}); c.Status != "fail" {
		t.Fatalf("empty dir: %+v", c)
	}
	two := &a458Adapter{listFn: func(string) ([]storage.FileInfo, error) {
		return []storage.FileInfo{{Path: "a"}, {Path: "b"}}, nil
	}}
	if c := checkRestorePointPresent(two, plain, db.RestorePoint{StoragePath: "rp"}); c.Status != "ok" || c.Detail != "2 files" {
		t.Fatalf("two files: %+v", c)
	}
}

func TestCheckDecryptableNoStoredHashA458(t *testing.T) {
	r, d := a458Runner(t)
	a458SetSetting(t, d, "encryption_passphrase_hash", "")
	if c := r.checkDecryptable(db.Job{Encryption: "age"}, "pw"); c.Status != "warn" {
		t.Fatalf("got %+v, want warn", c)
	}
}

func TestCheckFreeSpaceOverrideAndErrorA458(t *testing.T) {
	r, d := a458Runner(t)
	override := t.TempDir()
	a458SetSetting(t, d, "staging_dir_override", override)

	var probed string
	orig := freeSpaceAt
	t.Cleanup(func() { freeSpaceAt = orig })
	freeSpaceAt = func(p string) (int64, error) { probed = p; return 1 << 30, nil }
	if c := r.checkFreeSpace("", db.RestorePoint{}); c.Status != "ok" || probed != override {
		t.Fatalf("override: %+v probed %q", c, probed)
	}

	freeSpaceAt = func(string) (int64, error) { return 0, errors.New("statfs failed") }
	if c := r.checkFreeSpace("/x", db.RestorePoint{}); c.Status != "skip" {
		t.Fatalf("error: %+v, want skip", c)
	}
}

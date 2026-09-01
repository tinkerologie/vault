package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// latestRestoreRun returns the most recent restore run recorded for a job.
func latestRestoreRun(t *testing.T, d *db.DB, jobID int64) db.JobRun {
	t.Helper()
	runs, err := d.GetJobRuns(jobID, 20)
	if err != nil {
		t.Fatalf("GetJobRuns: %v", err)
	}
	var newest db.JobRun
	for _, run := range runs {
		if run.RunType == "restore" && run.ID > newest.ID {
			newest = run
		}
	}
	if newest.ID == 0 {
		t.Fatal("no restore run found")
	}
	return newest
}

// seedTwoItemBackup creates a job with two folder items of deliberately
// different sizes, runs one full backup, and returns the job ID, the restore
// point, and the per-item sizes recorded for it.
func seedTwoItemBackup(t *testing.T) (*Runner, *db.DB, int64, db.RestorePoint) {
	t.Helper()
	r, d := newTestRunner(t)

	smallDir, largeDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(smallDir, "small.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Large enough that the two items can never be confused by size.
	if err := os.WriteFile(filepath.Join(largeDir, "large.txt"), make([]byte, 64*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	destCfg, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "restore-size-" + nextUniqueRunner(t), Type: "local", Config: string(destCfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name:            "restore-size-job-" + nextUniqueRunner(t),
		StorageDestID:   destID,
		BackupTypeChain: "full",
		Compression:     "none",
		Encryption:      "none",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	for name, dir := range map[string]string{"small": smallDir, "large": largeDir} {
		settings, _ := json.Marshal(map[string]any{"path": dir})
		if _, err := d.AddJobItem(db.JobItem{
			JobID: jobID, ItemType: "folder", ItemName: name, Settings: string(settings),
		}); err != nil {
			t.Fatalf("add item %s: %v", name, err)
		}
	}

	r.RunJob(jobID)

	rps, err := d.ListRestorePoints(jobID)
	if err != nil || len(rps) == 0 {
		t.Fatalf("expected a restore point, got %d (err=%v)", len(rps), err)
	}
	return r, d, jobID, rps[0]
}

// TestRunRestore_SizeReflectsRestoredSubset is the regression guard for #334:
// restoring one item out of two must report that item's size, not the whole
// restore point's. It also pins the itemSizes-keyed-by-item-name seam — if the
// keys ever drift from the restore-target names the sum collapses to zero and
// the fallback silently re-inflates the reported size.
func TestRunRestore_SizeReflectsRestoredSubset(t *testing.T) {
	t.Parallel()
	r, d, jobID, rp := seedTwoItemBackup(t)

	sizes, ok := rp.ItemSizes()
	if !ok {
		t.Fatal("restore point recorded no item sizes")
	}
	wantSize, found := sizes["small"]
	if !found || wantSize <= 0 {
		t.Fatalf("item_sizes = %v, want a positive entry for \"small\"", sizes)
	}
	if rp.SizeBytes <= wantSize {
		t.Fatalf("restore point total %d does not exceed the single item's %d — the test cannot distinguish them", rp.SizeBytes, wantSize)
	}

	r.RunRestore(rp, []RestoreTarget{{Name: "small", Type: "folder"}}, t.TempDir(), "")

	run := latestRestoreRun(t, d, jobID)
	if run.SizeBytes != wantSize {
		t.Errorf("run size = %d, want %d (restore point total is %d)", run.SizeBytes, wantSize, rp.SizeBytes)
	}
}

// TestRunRestore_SizeFallsBackWithoutItemSizes covers the other branch: a
// restore point predating the item_sizes metadata reports the point total
// rather than zero, and says so in the run log.
func TestRunRestore_SizeFallsBackWithoutItemSizes(t *testing.T) {
	t.Parallel()
	r, d, jobID, rp := seedTwoItemBackup(t)

	// Strip item_sizes the way a legacy restore point would have been written.
	var meta map[string]any
	if err := json.Unmarshal([]byte(rp.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	delete(meta, "item_sizes")
	stripped, _ := json.Marshal(meta)
	legacy := rp
	legacy.Metadata = string(stripped)
	if _, ok := legacy.ItemSizes(); ok {
		t.Fatal("item_sizes survived the strip")
	}

	r.RunRestore(legacy, []RestoreTarget{{Name: "small", Type: "folder"}}, t.TempDir(), "")

	run := latestRestoreRun(t, d, jobID)
	if run.SizeBytes != rp.SizeBytes {
		t.Errorf("run size = %d, want the restore point total %d", run.SizeBytes, rp.SizeBytes)
	}
	if run.ItemsDone == 0 {
		t.Fatalf("expected the item to restore successfully, run = %+v", run)
	}
}

// TestRunRestore_SizeStaysZeroWhenNothingRestored pins the guard CodeRabbit
// asked for: when every target fails there is nothing restored, so the
// fallback must not report the restore point's total.
func TestRunRestore_SizeStaysZeroWhenNothingRestored(t *testing.T) {
	t.Parallel()
	r, d, jobID, rp := seedTwoItemBackup(t)

	broken := rp
	broken.StoragePath = "no-such-path"
	r.RunRestore(broken, []RestoreTarget{{Name: "small", Type: "folder"}}, t.TempDir(), "")

	run := latestRestoreRun(t, d, jobID)
	if run.ItemsDone != 0 {
		t.Fatalf("expected the restore to fail, items_done = %d", run.ItemsDone)
	}
	if run.SizeBytes != 0 {
		t.Errorf("run size = %d, want 0 when no item was restored", run.SizeBytes)
	}
	if !strings.EqualFold(run.Status, "failed") {
		t.Errorf("status = %q, want failed", run.Status)
	}
}

// TestRunRestore_ZeroItemSizeReportsZero pins the writer/reader contract for an
// item that backed up nothing: item_sizes carries an explicit 0 (rather than
// omitting the item), and the restore treats that 0 as a known size instead of
// falling through to the restore point's total.
//
// The zero is injected rather than produced by a real empty backup: a folder
// item always writes a tar header, so no folder backup reports 0 bytes. The
// case the writer guards is an item whose engine reports nothing — a container
// with every volume skipped, for instance.
func TestRunRestore_ZeroItemSizeReportsZero(t *testing.T) {
	t.Parallel()
	r, d, jobID, rp := seedTwoItemBackup(t)

	var meta map[string]any
	if err := json.Unmarshal([]byte(rp.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	sizes, ok := meta["item_sizes"].(map[string]any)
	if !ok {
		t.Fatalf("item_sizes missing from metadata %s", rp.Metadata)
	}
	sizes["small"] = 0
	rewritten, _ := json.Marshal(meta)
	zeroed := rp
	zeroed.Metadata = string(rewritten)

	parsed, found := zeroed.ItemSizes()
	if !found {
		t.Fatal("ItemSizes dropped the map once an entry was zero")
	}
	if size, present := parsed["small"]; !present || size != 0 {
		t.Fatalf("item_sizes[small] = %d (present=%v), want an explicit 0", size, present)
	}

	r.RunRestore(zeroed, []RestoreTarget{{Name: "small", Type: "folder"}}, t.TempDir(), "")

	run := latestRestoreRun(t, d, jobID)
	if run.ItemsDone == 0 {
		t.Fatalf("expected the item to restore successfully, run = %+v", run)
	}
	if run.SizeBytes != 0 {
		t.Errorf("run size = %d, want 0 — a recorded zero is a known size, not a missing one (point total is %d)", run.SizeBytes, rp.SizeBytes)
	}
}

// TestCollectItemSizes pins what counts as a known size in the item_sizes
// metadata: an explicit zero is kept so a restore of that item alone reports 0,
// while a missing, non-numeric, or negative size is dropped so the restore
// falls back rather than claiming the item moved nothing.
func TestCollectItemSizes(t *testing.T) {
	t.Parallel()
	got := collectItemSizes([]map[string]any{
		{"name": "normal", "size_bytes": int64(4096)},
		{"name": "empty", "size_bytes": int64(0)},
		{"name": "unreported"},
		{"name": "not-a-number", "size_bytes": "4096"},
		{"name": "float-from-json", "size_bytes": float64(4096)},
		{"name": "negative", "size_bytes": int64(-1)},
		{"size_bytes": int64(99)},
	})
	want := map[string]int64{"normal": 4096, "empty": 0}
	if len(got) != len(want) {
		t.Fatalf("collectItemSizes = %v, want %v", got, want)
	}
	for name, size := range want {
		if got[name] != size {
			t.Errorf("%s = %d, want %d", name, got[name], size)
		}
	}
}

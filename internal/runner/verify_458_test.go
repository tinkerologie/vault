package runner

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
)

// c458StreamErrAdapter returns a reader that fails mid-stream so the deep
// verify hashing branch reports a stream error.
type c458StreamErrAdapter struct {
	storage.Adapter
}

func (c458StreamErrAdapter) Stat(p string) (storage.FileInfo, error) {
	return storage.FileInfo{Path: p, Size: 4}, nil
}

func (c458StreamErrAdapter) Read(string) (io.ReadCloser, error) {
	return io.NopCloser(c458ErrReader{}), nil
}

type c458ErrReader struct{}

func (c458ErrReader) Read([]byte) (int, error) { return 0, errors.New("disk vanished") }

func c458VerifyRow(t *testing.T, d *db.DB, rpID int64) int64 {
	t.Helper()
	id, err := d.CreateVerifyRun(rpID, string(VerifyModeQuick))
	if err != nil {
		t.Fatalf("CreateVerifyRun: %v", err)
	}
	return id
}

func c458VerifyResult(t *testing.T, d *db.DB, id int64) db.VerifyRun {
	t.Helper()
	run, err := d.GetVerifyRun(id)
	if err != nil {
		t.Fatalf("GetVerifyRun: %v", err)
	}
	return run
}

func TestVerifyOneFileStreamErrorC458(t *testing.T) {
	r, _, _ := setupTestRunner(t)
	res := r.verifyOneFile(c458StreamErrAdapter{}, "p", recordedChecksum{}, VerifyModeDeep)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "stream") {
		t.Fatalf("want stream error, got %v", res.Err)
	}
}

func TestRunVerifyMissingJobAndDestC458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	if _, err := r.RunVerify(db.RestorePoint{ID: 1, JobID: 999}, VerifyModeQuick); err == nil {
		t.Fatal("want job lookup error")
	}
	dest := createLocalDest(t, d, storageDir)
	jobID, err := d.CreateJob(db.Job{Name: "c458-verify-nodest", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteStorageDestination(dest.ID); err != nil {
		t.Skipf("cannot delete destination with jobs attached: %v", err)
	}
	if _, err := r.RunVerify(db.RestorePoint{ID: 1, JobID: jobID}, VerifyModeQuick); err == nil || !strings.Contains(err.Error(), "storage destination") {
		t.Fatalf("want destination lookup error, got %v", err)
	}
}

func TestRunVerifyLoopClassicFailuresC458(t *testing.T) {
	r, d, storageDir := setupTestRunner(t)
	jobID, dest := seedVerifiableJob(t, d, storageDir, "c458-verify-classic")
	rps, err := d.ListRestorePoints(jobID)
	if err != nil || len(rps) == 0 {
		t.Fatalf("restore points: %v (%d)", err, len(rps))
	}
	rp := rps[0]

	// Unknown storage type: adapter construction fails.
	id := c458VerifyRow(t, d, rp.ID)
	r.runVerifyLoop(id, rp, VerifyModeQuick, db.StorageDestination{Type: "bogus", Config: "{}"})
	if run := c458VerifyResult(t, d, id); run.Status != "failed" || !strings.Contains(run.ErrorSummary, "storage adapter") {
		t.Fatalf("bogus adapter: %+v", run)
	}

	// Malformed metadata: checksum parse fails.
	id = c458VerifyRow(t, d, rp.ID)
	bad := rp
	bad.Metadata = "{not json"
	r.runVerifyLoop(id, bad, VerifyModeQuick, dest)
	if run := c458VerifyResult(t, d, id); run.Status != "failed" || !strings.Contains(run.ErrorSummary, "parsing recorded checksums") {
		t.Fatalf("bad metadata: %+v", run)
	}

	// Many recorded files, all missing on storage: every one fails and the
	// progress branch fires repeatedly.
	id = c458VerifyRow(t, d, rp.ID)
	many := rp
	many.Metadata = `{"checksums":{"item":{"a":"00","b":"00","c":"00","d":"00","e":"00","f":"00"}}}`
	r.runVerifyLoop(id, many, VerifyModeDeep, dest)
	if run := c458VerifyResult(t, d, id); run.Status != "failed" || run.FilesFailed != 6 {
		t.Fatalf("missing files: %+v", run)
	}
}

func TestRunVerifyLoopDedupFailuresC458(t *testing.T) {
	d, r, rp, storageDir := setupDedupVerifyFixture(t)
	job, err := d.GetJob(rp.JobID)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := d.GetStorageDestination(job.StorageDestID)
	if err != nil {
		t.Fatal(err)
	}

	// Adapter construction fails on the dedup branch.
	id := c458VerifyRow(t, d, rp.ID)
	r.runVerifyLoop(id, rp, VerifyModeQuick, db.StorageDestination{ID: dest.ID, Type: "bogus", Config: "{}", DedupEnabled: true})
	if run := c458VerifyResult(t, d, id); !strings.Contains(run.ErrorSummary, "storage adapter") {
		t.Fatalf("bogus adapter: %+v", run)
	}

	// Unknown mode is rejected after the closure walk.
	id = c458VerifyRow(t, d, rp.ID)
	r.runVerifyLoopDedup(id, rp, VerifyMode("sideways"), dest)
	if run := c458VerifyResult(t, d, id); !strings.Contains(run.ErrorSummary, "unknown verify mode") {
		t.Fatalf("unknown mode: %+v", run)
	}

	// No manifest IDs at all.
	id = c458VerifyRow(t, d, rp.ID)
	none := rp
	none.ManifestID = nil
	none.Metadata = `{"item_manifests":{"x":"zz-not-hex","y":"abcd"}}`
	r.runVerifyLoopDedup(id, none, VerifyModeQuick, dest)
	if run := c458VerifyResult(t, d, id); !strings.Contains(run.ErrorSummary, "no manifest IDs") {
		t.Fatalf("no manifests: %+v", run)
	}

	// A manifest ID that is not in the repo fails the closure walk.
	id = c458VerifyRow(t, d, rp.ID)
	ghost := rp
	ghost.ManifestID = make([]byte, 32)
	ghost.Metadata = ""
	r.runVerifyLoopDedup(id, ghost, VerifyModeQuick, dest)
	if run := c458VerifyResult(t, d, id); !strings.Contains(run.ErrorSummary, "walking manifest closure") {
		t.Fatalf("ghost manifest: %+v", run)
	}

	// Remove packs after the index is loaded: quick mode cannot stat them.
	packsDir := filepath.Join(storageDir, "_vault", "packs")
	if _, err := os.Stat(packsDir); err != nil {
		t.Skipf("packs dir layout changed: %v", err)
	}
	moved := packsDir + ".moved"
	if err := os.Rename(packsDir, moved); err != nil {
		t.Fatal(err)
	}
	id = c458VerifyRow(t, d, rp.ID)
	r.runVerifyLoopDedup(id, rp, VerifyModeQuick, dest)
	// The manifest itself lives in a pack, so the closure walk fails first;
	// either way the run must fail.
	if run := c458VerifyResult(t, d, id); run.Status != "failed" {
		t.Fatalf("missing packs should fail verify: %+v", run)
	}
	_ = os.Rename(moved, packsDir)

	// Repo header gone: opening the repo fails.
	if err := os.Rename(filepath.Join(storageDir, "_vault"), filepath.Join(storageDir, "_gone")); err != nil {
		t.Fatal(err)
	}
	id = c458VerifyRow(t, d, rp.ID)
	r.runVerifyLoopDedup(id, rp, VerifyModeQuick, dest)
	if run := c458VerifyResult(t, d, id); !strings.Contains(run.ErrorSummary, "open dedup repo") {
		t.Fatalf("missing repo: %+v", run)
	}
}

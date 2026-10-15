package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
)

// c458RangeFailAdapter fails every ReadRange so objectIsCompressed takes its
// conservative branch.
type c458RangeFailAdapter struct{ storage.Adapter }

func (c458RangeFailAdapter) ReadRange(string, int64, int64) (io.ReadCloser, error) {
	return nil, errors.New("range refused")
}

func c458Encrypt(t *testing.T, pass, content string) []byte {
	t.Helper()
	enc, err := crypto.EncryptReader(pass, strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	b, err := io.ReadAll(enc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestObjectIsCompressedRangeErrorC458(t *testing.T) {
	r, _, _ := setupTestRunner(t)
	if !r.objectIsCompressed(c458RangeFailAdapter{}, "x") {
		t.Fatal("range failure must be treated as compressed")
	}
}

func TestSha256FileErrorsC458(t *testing.T) {
	if _, err := sha256File(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file should error")
	}
	if _, err := sha256File(t.TempDir()); err == nil {
		t.Fatal("hashing a directory should error")
	}
}

func TestDownloadSequentialResumableBranchesC458(t *testing.T) {
	r, _, _ := setupTestRunner(t)
	ctx := context.Background()
	enc := c458Encrypt(t, "pw", "secret payload")
	a := &stagingAdapter{files: map[string][]byte{
		"rp/item/good.bin.age": enc,
		"rp/item/bad.bin.age":  []byte("not an age file at all"),
		"rp/item/plain.bin":    []byte("plain"),
		"rp/item/broken.gz":    append([]byte{0x1f, 0x8b, 0x08, 0x00}, bytes.Repeat([]byte{0xff}, 64)...),
	}}
	fi := func(p string) storage.FileInfo {
		return storage.FileInfo{Path: p, Size: int64(len(a.files[p]))}
	}

	// Encrypted object decrypts to its plain name.
	dir := t.TempDir()
	if err := r.downloadSequentialResumable(ctx, a, fi("rp/item/good.bin.age"), dir, "pw", "none", nil, nil); err != nil {
		t.Fatalf("encrypted download: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "good.bin")); err != nil || string(b) != "secret payload" {
		t.Fatalf("decrypted = %q, %v", b, err)
	}

	// Garbage .age content fails decryption.
	if err := r.downloadSequentialResumable(ctx, a, fi("rp/item/bad.bin.age"), t.TempDir(), "pw", "none", nil, nil); err == nil {
		t.Fatal("garbage age content should fail")
	}

	// Local target directory missing.
	if err := r.downloadSequentialResumable(ctx, a, fi("rp/item/plain.bin"), filepath.Join(t.TempDir(), "nope"), "", "none", nil, nil); err == nil || !strings.Contains(err.Error(), "creating local file") {
		t.Fatalf("missing tmp dir: %v", err)
	}

	// Corrupt gzip stream fails during decompression or copy.
	if err := r.downloadSequentialResumable(ctx, a, fi("rp/item/broken.gz"), t.TempDir(), "", "gzip", nil, nil); err == nil {
		t.Fatal("corrupt gzip should fail")
	}
}

func TestDownloadParallelPlainBranchesC458(t *testing.T) {
	r, _, _ := setupTestRunner(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("abcdefgh"), 8)
	a := &stagingAdapter{files: map[string][]byte{"rp/item/big.bin": data}}

	// Missing local directory.
	fi := storage.FileInfo{Path: "rp/item/big.bin", Size: int64(len(data))}
	if err := r.downloadParallelPlain(ctx, a, fi, filepath.Join(t.TempDir(), "nope"), nil, nil, 16, 2); err == nil || !strings.Contains(err.Error(), "creating local file") {
		t.Fatalf("missing dir: %v", err)
	}

	// Recorded size larger than the object: a ranged read fails.
	tooBig := storage.FileInfo{Path: "rp/item/big.bin", Size: int64(len(data)) * 4}
	if err := r.downloadParallelPlain(ctx, a, tooBig, t.TempDir(), nil, nil, 16, 2); err == nil || !strings.Contains(err.Error(), "downloading") {
		t.Fatalf("oversized: %v", err)
	}

	// Checksum mismatch after a successful assembly.
	if err := r.downloadParallelPlain(ctx, a, fi, t.TempDir(), map[string]string{"big.bin": "00"}, nil, 16, 2); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("mismatch: %v", err)
	}
}

func TestStageRestorePointItemErrorsC458(t *testing.T) {
	ctx := context.Background()
	rep := restoreProgressReporter{ItemName: "item", ItemsTotal: 1}

	r, d, storageDir := setupTestRunner(t)
	// Missing job.
	if err := r.stageRestorePointItem(ctx, db.RestorePoint{JobID: 4242}, "item", t.TempDir(), "", 0, 40, rep); err == nil || !strings.Contains(err.Error(), "getting job") {
		t.Fatalf("missing job: %v", err)
	}
	// Destination that cannot build an adapter.
	badID, err := d.CreateStorageDestination(db.StorageDestination{Name: "c458-bogus-stage", Type: "bogus", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	badJob, err := d.CreateJob(db.Job{Name: "c458-bogus-stage", StorageDestID: badID})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.stageRestorePointItem(ctx, db.RestorePoint{JobID: badJob}, "item", t.TempDir(), "", 0, 40, rep); err == nil || !strings.Contains(err.Error(), "creating storage adapter") {
		t.Fatalf("bogus adapter: %v", err)
	}

	// Item directory with a nested dir and a file whose checksum mismatches.
	dest := createLocalDest(t, d, storageDir)
	jobID, err := d.CreateJob(db.Job{Name: "c458-stage", StorageDestID: dest.ID, Compression: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(storageDir, "c458-stage", "run", "item", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "c458-stage", "run", "item", "f.bin"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"checksums": map[string]any{"item": map[string]string{"f.bin": "00"}}})
	rp := db.RestorePoint{JobID: jobID, StoragePath: "c458-stage/run", Metadata: string(meta)}
	if err := r.stageRestorePointItem(ctx, rp, "item", t.TempDir(), "", 0, 40, rep); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("mismatch: %v", err)
	}

	// Pre-cancelled context: no downloads start and ctx.Err is returned.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	rp.Metadata = "{}"
	if err := r.stageRestorePointItem(cctx, rp, "item", t.TempDir(), "", 0, 40, rep); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

// c458ChainFixture returns a runner + two-step chain of a single folder item
// stored on a local destination.
func c458ChainFixture(t *testing.T, item string) (*Runner, *db.DB, []db.RestorePoint, storage.Adapter) {
	t.Helper()
	r, d, storageDir := setupTestRunner(t)
	if err := d.SetSetting("staging_dir_override", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	dest := createLocalDest(t, d, storageDir)
	jobID, err := d.CreateJob(db.Job{Name: "c458-chain", StorageDestID: dest.ID, BackupTypeChain: "incremental"})
	if err != nil {
		t.Fatal(err)
	}
	chain := []db.RestorePoint{
		{ID: 1, JobID: jobID, BackupType: "full", StoragePath: "c458-chain/1", Metadata: `{"item_sizes":{"` + item + `":1}}`, CreatedAt: time.Now().Add(-time.Hour)},
		{ID: 2, JobID: jobID, BackupType: "incremental", StoragePath: "c458-chain/2", ParentRestorePointID: 1, Metadata: `{"item_sizes":{"` + item + `":1}}`, CreatedAt: time.Now()},
	}
	return r, d, chain, c458Adapter(t, dest)
}

func TestRestoreMergedChainVMPathC458(t *testing.T) {
	c458FakeQemuImg(t, "")
	r, _, chain, a := c458ChainFixture(t, "vm1")
	writeStorageFiles(t, a, map[string]string{
		"c458-chain/1/vm1/vm_meta.json": c458TwoStepMetaFull,
		"c458-chain/1/vm1/vdisk0.img":   "base",
		"c458-chain/2/vm1/vm_meta.json": c458TwoStepMetaInc,
		"c458-chain/2/vm1/vdisk0.qcow2": "top",
		"c458-chain/2/vm1/domain.xml":   "<domain/>",
	})
	rep := restoreProgressReporter{ItemName: "vm1", ItemType: "vm", ItemsTotal: 1}
	// Staging + flatten succeed; the VM handler itself cannot restore on a
	// test host (no libvirt), so an error from restoreStagedItem is expected.
	err := r.restoreMergedChain(context.Background(), chain, "vm1", "vm", t.TempDir(), "", nil, false, rep)
	if err == nil {
		t.Skip("VM restore unexpectedly succeeded on this host")
	}
	if strings.Contains(err.Error(), "staging VM chain") || strings.Contains(err.Error(), "flattening VM chain") {
		t.Fatalf("expected handler-level failure, got %v", err)
	}
}

func TestRestoreMergedChainVMFailuresC458(t *testing.T) {
	r, _, chain, a := c458ChainFixture(t, "vm1")
	rep := restoreProgressReporter{ItemName: "vm1", ItemType: "vm", ItemsTotal: 1}

	// No step captured the item.
	if err := r.restoreMergedChain(context.Background(), chain, "other", "vm", t.TempDir(), "", nil, false, rep); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("absent item: %v", err)
	}

	// Steps stage (empty) but flattening finds no resolvable disks.
	writeStorageFiles(t, a, map[string]string{
		"c458-chain/2/vm1/vm_meta.json": `{"disks":[{"target":"","backup_file":"x"}]}`,
	})
	if err := r.restoreMergedChain(context.Background(), chain, "vm1", "vm", t.TempDir(), "", nil, false, rep); err == nil || !strings.Contains(err.Error(), "flattening VM chain") {
		t.Fatalf("flatten failure: %v", err)
	}

	// Staging failure (unknown job).
	broken := []db.RestorePoint{chain[0], chain[1]}
	broken[0].JobID = 777
	if err := r.restoreMergedChain(context.Background(), broken, "vm1", "vm", t.TempDir(), "", nil, false, rep); err == nil || !strings.Contains(err.Error(), "staging VM chain step") {
		t.Fatalf("staging failure: %v", err)
	}
}

func TestRestoreMergedChainGenericAndContainerFailuresC458(t *testing.T) {
	r, _, chain, _ := c458ChainFixture(t, "f")
	rep := restoreProgressReporter{ItemName: "f", ItemType: "folder", ItemsTotal: 1}

	if err := r.restoreMergedChain(context.Background(), chain, "absent", "folder", t.TempDir(), "", nil, false, rep); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("absent folder: %v", err)
	}
	broken := []db.RestorePoint{chain[0], chain[1]}
	broken[0].JobID = 778
	if err := r.restoreMergedChain(context.Background(), broken, "f", "folder", t.TempDir(), "", nil, false, rep); err == nil || !strings.Contains(err.Error(), "staging chain step") {
		t.Fatalf("folder staging failure: %v", err)
	}
	if err := r.restoreMergedChain(context.Background(), broken, "f", "container", t.TempDir(), "", nil, false, rep); err == nil || !strings.Contains(err.Error(), "staging chain step") {
		t.Fatalf("container staging failure: %v", err)
	}
	if err := r.restoreMergedChain(context.Background(), chain, "absent", "container", t.TempDir(), "", nil, false, rep); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("container absent: %v", err)
	}
}

func TestRestoreSinglePointStagingErrorC458(t *testing.T) {
	r, d, _ := setupTestRunner(t)
	if err := d.SetSetting("staging_dir_override", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	rep := restoreProgressReporter{ItemName: "x", ItemType: "folder", ItemsTotal: 1}
	if err := r.restoreSinglePoint(context.Background(), db.RestorePoint{JobID: 31337}, "x", "folder", t.TempDir(), "", nil, false, rep); err == nil {
		t.Fatal("missing job should fail")
	}
}

func TestRestoreSinglePointChunkedBranchesC458(t *testing.T) {
	_, r, rp, storageDir := setupDedupVerifyFixture(t)
	rep := restoreProgressReporter{ItemName: "src", ItemType: "folder", ItemsTotal: 1}
	var mid [32]byte
	copy(mid[:], rp.ManifestID)

	// Missing job.
	ghost := rp
	ghost.JobID = 999
	if err := r.restoreSinglePointChunked(context.Background(), ghost, mid, "src", "folder", t.TempDir(), nil, false, rep); err == nil {
		t.Fatal("missing job should fail")
	}

	// Unknown handler type.
	if err := r.restoreSinglePointChunked(context.Background(), rp, mid, "src", "bogus-type", t.TempDir(), nil, false, rep); err == nil {
		t.Fatal("unknown type should fail")
	}

	// Partial restore into an explicit destination.
	out := t.TempDir()
	if err := r.restoreSinglePointChunked(context.Background(), rp, mid, "src", "folder", out, []string{"a.txt"}, false, rep); err != nil {
		t.Fatalf("partial restore: %v", err)
	}

	// Empty destination: the folder's original path is resolved from the
	// job item and the restore writes back there.
	if err := r.restoreSinglePointChunked(context.Background(), rp, mid, "src", "folder", "", nil, false, rep); err != nil {
		t.Fatalf("restore to original path: %v", err)
	}

	// Corrupt repo header: opening it fails.
	if err := os.WriteFile(filepath.Join(storageDir, "_vault", "repo.json"), []byte("{garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.restoreSinglePointChunked(context.Background(), rp, mid, "src", "folder", t.TempDir(), nil, false, rep); err == nil || !strings.Contains(err.Error(), "open dedup repo") {
		t.Fatalf("missing repo: %v", err)
	}
}

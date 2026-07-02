package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestUploadStagedFiles_UpdatesLastProgress verifies the upload path heartbeats
// the stall watchdog as bytes flow (issue #110). Before the fix lastProgress
// only advanced on a retry, so a long blocking upload froze the no-progress
// timer and the watchdog cancelled healthy backups.
func TestUploadStagedFiles_UpdatesLastProgress(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "archive.tar"), bytes.Repeat([]byte("x"), 1<<20), 0600); err != nil {
		t.Fatalf("stage file: %v", err)
	}
	cfg, _ := json.Marshal(map[string]string{"path": filepath.Join(t.TempDir(), "store")})
	dest := db.StorageDestination{Type: "local", Config: string(cfg)}

	// Force the progress timestamp into the past; a working upload must move it.
	stale := time.Now().Add(-3 * time.Hour)
	r.lastProgressMu.Lock()
	r.lastProgress = stale
	r.lastProgressMu.Unlock()

	if _, err := r.uploadStagedFilesN(context.Background(), tmpDir, dest, "rp", false, "", "none", "folder", "Test", 1); err != nil {
		t.Fatalf("uploadStagedFiles: %v", err)
	}

	r.lastProgressMu.Lock()
	got := r.lastProgress
	r.lastProgressMu.Unlock()
	if !got.After(stale) {
		t.Errorf("lastProgress not advanced during upload (still %v) — stall watchdog would fire on a long upload", got)
	}
}

// TestUploadStagedFiles_BadAdapterConfig drives the storage.NewAdapter
// error branch by feeding a corrupt JSON config.
func TestUploadStagedFiles_BadAdapterConfig(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	dest := db.StorageDestination{
		Type:   "local",
		Config: `{not valid json`,
	}
	_, err := r.uploadStagedFilesN(context.Background(), t.TempDir(), dest, "rp", false, "", "none", "folder", "Test Folder", 1)
	if err == nil {
		t.Fatal("expected NewAdapter error for corrupt config")
	}
}

// TestUploadStagedFiles_MissingTmpDir drives the os.ReadDir error branch
// by pointing tmpDir at a path that doesn't exist.
func TestUploadStagedFiles_MissingTmpDir(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	cfg, _ := json.Marshal(map[string]string{"path": filepath.Join(t.TempDir(), "store")})
	dest := db.StorageDestination{
		Type:   "local",
		Config: string(cfg),
	}
	_, err := r.uploadStagedFilesN(context.Background(), filepath.Join(t.TempDir(), "no-such-dir"), dest, "rp", false, "", "none", "folder", "Test", 1)
	if err == nil {
		t.Fatal("expected ReadDir error for missing tmpDir")
	}
}

// TestUploadStagedFiles_EmptyTmpDirReturnsEmptyChecksums drives the
// happy path where tmpDir exists but is empty: the for-loop is a no-op
// and the function returns an empty checksums map with no error.
func TestUploadStagedFiles_EmptyTmpDirReturnsEmptyChecksums(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	storageDir := filepath.Join(t.TempDir(), "store")
	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	dest := db.StorageDestination{
		Type:   "local",
		Config: string(cfg),
	}
	checksums, err := r.uploadStagedFilesN(context.Background(), t.TempDir(), dest, "rp", false, "", "none", "folder", "Test", 1)
	if err != nil {
		t.Fatalf("uploadStagedFiles(empty tmpDir): %v", err)
	}
	if len(checksums) != 0 {
		t.Errorf("expected empty checksums for empty tmpDir, got %d entries", len(checksums))
	}
}

// TestUploadStagedFiles_HappyPath_NoEncryption drives the file-upload
// loop body: a small file is uploaded, a SHA-256 checksum is computed
// and recorded, and the resulting checksums map contains exactly one
// entry. No encryption (passphrase=""), no verify.
func TestUploadStagedFiles_HappyPath_NoEncryption(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	tmpDir := t.TempDir()
	if err := writeFileSafe(t, filepath.Join(tmpDir, "volume_0.tar"), []byte("payload-A")); err != nil {
		t.Fatalf("setup file: %v", err)
	}

	storageDir := filepath.Join(t.TempDir(), "store")
	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	dest := db.StorageDestination{
		Type:   "local",
		Config: string(cfg),
	}
	checksums, err := r.uploadStagedFilesN(context.Background(), tmpDir, dest, "rp-x", false, "", "none", "folder", "Test", 1)
	if err != nil {
		t.Fatalf("uploadStagedFiles: %v", err)
	}
	if len(checksums) != 1 {
		t.Errorf("expected exactly 1 checksum entry, got %d", len(checksums))
	}
	if _, ok := checksums["volume_0.tar"]; !ok {
		t.Errorf("expected checksums key 'volume_0.tar', got %v", checksums)
	}
}

// TestUploadStagedFiles_HappyPath_WithEncryption drives the same path
// but with a passphrase set: the storage filename gets a `.age` suffix
// and the on-disk file is age-encrypted.
func TestUploadStagedFiles_HappyPath_WithEncryption(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	tmpDir := t.TempDir()
	if err := writeFileSafe(t, filepath.Join(tmpDir, "image.tar"), []byte("encrypted-payload")); err != nil {
		t.Fatalf("setup file: %v", err)
	}

	storageDir := filepath.Join(t.TempDir(), "store")
	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	dest := db.StorageDestination{
		Type:   "local",
		Config: string(cfg),
	}
	checksums, err := r.uploadStagedFilesN(context.Background(), tmpDir, dest, "rp-y", false, "secret", "none", "container", "MyContainer", 1)
	if err != nil {
		t.Fatalf("uploadStagedFiles: %v", err)
	}
	// Filename in checksums map should carry the .age suffix.
	if _, ok := checksums["image.tar.age"]; !ok {
		t.Errorf("expected checksums key 'image.tar.age', got %v", checksums)
	}
}

// TestUploadStagedFiles_ParallelAllChecksums verifies that with a concurrency
// of N every staged file is uploaded and its checksum recorded, regardless of
// completion order. Exercises the bounded worker pool added for parallel
// uploads.
func TestUploadStagedFiles_ParallelAllChecksums(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)
	tmp := t.TempDir()
	for i := 0; i < 6; i++ {
		name := filepath.Join(tmp, "vol"+strconv.Itoa(i)+".tar")
		if err := os.WriteFile(name, bytes.Repeat([]byte{byte(i)}, 4096), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ := json.Marshal(map[string]string{"path": filepath.Join(t.TempDir(), "store")})
	dest := db.StorageDestination{Type: "local", Config: string(cfg)}

	sums, err := r.uploadStagedFilesN(context.Background(), tmp, dest, "rp", false, "", "none", "folder", "Test", 3)
	if err != nil {
		t.Fatalf("uploadStagedFilesN: %v", err)
	}
	if len(sums) != 6 {
		t.Errorf("checksums = %d, want 6", len(sums))
	}
}

// TestUploadStagedFiles_VMTransportCompression verifies the job's compression
// setting is applied to VM uploads (issue: qcow2 files landed uncompressed on
// the destination even with gzip/zstd configured) while engine-compressed
// item types (container/folder/plugin archives) are still uploaded as-is.
func TestUploadStagedFiles_VMTransportCompression(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)

	tmpDir := t.TempDir()
	payload := bytes.Repeat([]byte("fedora vm disk block "), 4096)
	if err := os.WriteFile(filepath.Join(tmpDir, "vdisk0.qcow2"), payload, 0600); err != nil {
		t.Fatalf("stage file: %v", err)
	}
	storeDir := filepath.Join(t.TempDir(), "store")
	cfg, _ := json.Marshal(map[string]string{"path": storeDir})
	dest := db.StorageDestination{Type: "local", Config: string(cfg)}

	checksums, err := r.uploadStagedFilesN(context.Background(), tmpDir, dest, "rp-vm", false, "", "zstd", "vm", "Fedora", 1)
	if err != nil {
		t.Fatalf("uploadStagedFiles: %v", err)
	}
	if _, ok := checksums["vdisk0.qcow2.zst"]; !ok {
		t.Fatalf("expected checksum keyed by stored name vdisk0.qcow2.zst, got %v", checksums)
	}

	stored, err := os.ReadFile(filepath.Join(storeDir, "rp-vm", "vdisk0.qcow2.zst"))
	if err != nil {
		t.Fatalf("stored file missing: %v", err)
	}
	if !looksCompressed(stored) {
		t.Fatal("stored VM disk is not compressed")
	}
	if len(stored) >= len(payload) {
		t.Fatalf("stored VM disk did not shrink: %d >= %d", len(stored), len(payload))
	}

	// Container archives are compressed by the engine — the upload layer
	// must not wrap them a second time.
	tmpDir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir2, "data.tar.zst"), payload, 0600); err != nil {
		t.Fatalf("stage file: %v", err)
	}
	checksums, err = r.uploadStagedFilesN(context.Background(), tmpDir2, dest, "rp-ct", false, "", "zstd", "container", "app", 1)
	if err != nil {
		t.Fatalf("uploadStagedFiles: %v", err)
	}
	if _, ok := checksums["data.tar.zst"]; !ok {
		t.Fatalf("container archive must be stored under its own name, got %v", checksums)
	}
}

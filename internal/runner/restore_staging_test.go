package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// stagingAdapter serves List/Read/ReadRange from an in-memory file map.
// It implements storage.Adapter for use in staging tests.
type stagingAdapter struct {
	files map[string][]byte // path → content
}

func (a *stagingAdapter) Write(_ string, _ io.Reader) error {
	return fmt.Errorf("stagingAdapter: Write not impl")
}
func (a *stagingAdapter) WriteFrom(_ string, _ func() (io.ReadCloser, error)) error {
	return fmt.Errorf("stagingAdapter: WriteFrom not impl")
}
func (a *stagingAdapter) Read(path string) (io.ReadCloser, error) {
	data, ok := a.files[path]
	if !ok {
		return nil, fmt.Errorf("stagingAdapter: not found: %s", path)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (a *stagingAdapter) ReadRange(path string, offset, length int64) (io.ReadCloser, error) {
	data, ok := a.files[path]
	if !ok {
		return nil, fmt.Errorf("stagingAdapter: not found: %s", path)
	}
	if offset < 0 || offset > int64(len(data)) {
		return nil, fmt.Errorf("stagingAdapter: bad offset %d for %s (len=%d)", offset, path, len(data))
	}
	end := offset + length
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return io.NopCloser(bytes.NewReader(data[offset:end])), nil
}
func (a *stagingAdapter) Delete(_ string) error { return fmt.Errorf("stagingAdapter: Delete not impl") }
func (a *stagingAdapter) List(prefix string) ([]storage.FileInfo, error) {
	var out []storage.FileInfo
	for p, data := range a.files {
		if strings.HasPrefix(p, prefix) {
			out = append(out, storage.FileInfo{Path: p, Size: int64(len(data))})
		}
	}
	return out, nil
}
func (a *stagingAdapter) Stat(path string) (storage.FileInfo, error) {
	data, ok := a.files[path]
	if !ok {
		return storage.FileInfo{}, fmt.Errorf("stagingAdapter: not found: %s", path)
	}
	return storage.FileInfo{Path: path, Size: int64(len(data))}, nil
}
func (a *stagingAdapter) TestConnection() error { return nil }
func (a *stagingAdapter) GetCapacity(_ context.Context) (storage.Capacity, error) {
	return storage.Capacity{}, nil
}
func (a *stagingAdapter) Usage() (int64, int64, error) { return 0, 0, fmt.Errorf("not impl") }

// writeZstd compresses b with zstd and returns the compressed bytes.
func writeZstd(b []byte) []byte {
	var buf bytes.Buffer
	w, _ := zstd.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

// sha256hexBytes returns the hex-encoded SHA-256 of b.
func sha256hexBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// setupStagingTest creates a Runner and a RestorePoint whose storage is backed
// by a real local adapter pre-populated with the given files.
// files keys are relative to storageDir (e.g. "staging-test-job/run1/myitem/archive.tar").
func setupStagingTest(t *testing.T, files map[string][]byte, metadata string) (*Runner, db.RestorePoint) {
	t.Helper()
	r, database, storageDir := setupTestRunner(t)

	for relPath, data := range files {
		fullPath := filepath.Join(storageDir, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", filepath.Dir(fullPath), err)
		}
		if err := os.WriteFile(fullPath, data, 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", fullPath, err)
		}
	}

	dest := createLocalDest(t, database, storageDir)
	jobID, err := database.CreateJob(db.Job{
		Name:          "staging-test-job",
		StorageDestID: dest.ID,
		Compression:   "none",
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	runID, err := database.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "completed", BackupType: "full",
	})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}
	if metadata == "" {
		metadata = "{}"
	}
	rpID, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "staging-test-job/run1",
		Metadata:    metadata,
	})
	if err != nil {
		t.Fatalf("CreateRestorePoint: %v", err)
	}
	rp, err := database.GetRestorePoint(rpID)
	if err != nil {
		t.Fatalf("GetRestorePoint: %v", err)
	}
	return r, rp
}

// buildChecksumsMetadata constructs a JSON metadata string with a checksum entry.
func buildChecksumsMetadata(t *testing.T, itemName, fileName, checksum string) string {
	t.Helper()
	meta := map[string]any{
		"checksums": map[string]any{
			itemName: map[string]any{
				fileName: checksum,
			},
		},
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal checksums metadata: %v", err)
	}
	return string(b)
}

// TestLooksCompressed verifies the looksCompressed helper.
func TestLooksCompressed(t *testing.T) {
	t.Parallel()
	if !looksCompressed([]byte{0x1f, 0x8b, 0x00, 0x00}) {
		t.Error("expected gzip magic to be detected as compressed")
	}
	if !looksCompressed([]byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Error("expected zstd magic to be detected as compressed")
	}
	if looksCompressed([]byte("plain data")) {
		t.Error("expected plain data to NOT be detected as compressed")
	}
	if looksCompressed(nil) {
		t.Error("expected nil to NOT be detected as compressed")
	}
	// 1-byte gzip prefix is insufficient
	if looksCompressed([]byte{0x1f}) {
		t.Error("expected 1-byte gzip prefix NOT to be detected as compressed")
	}
}

// TestSha256File verifies the sha256File helper against a known hash.
func TestSha256File(t *testing.T) {
	t.Parallel()
	data := []byte("hello vault")
	path := filepath.Join(t.TempDir(), "test.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := sha256File(path)
	if err != nil {
		t.Fatalf("sha256File: %v", err)
	}
	want := sha256hexBytes(data)
	if got != want {
		t.Errorf("sha256File = %q, want %q", got, want)
	}
}

// TestStagePlainLargeFileParallelPath verifies the parallel download path.
//
// Test-design decision: storage.RestorePartSize is 32 MiB; allocating 64+ MiB
// in a unit test is wasteful. Instead we call downloadParallelPlain directly
// with a small payload and a small partSize (1 KiB), which exercises
// ParallelRangeDownload fully (multi-part, WriteAt assembly, checksum verify)
// without the large allocation. The routing predicate (size >= 2*partSize) is
// tested separately in TestDownloadRestoreFileRouting with a controllable
// partSize. Production behaviour (32 MiB parts) is unchanged.
func TestStagePlainLargeFileParallelPath(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte("VAULTDATA"), 1000) // ~9 KB
	want := sha256hexBytes(data)

	r, _ := newTestRunner(t)
	sa := &stagingAdapter{
		files: map[string][]byte{"rp/item/disk.qcow2": data},
	}
	fi := storage.FileInfo{Path: "rp/item/disk.qcow2", Size: int64(len(data))}

	tmpDir := t.TempDir()
	var heartbeatBytes atomic.Int64
	onBytes := func(n int64) { heartbeatBytes.Add(n) }

	const smallPartSize = 1024 // 1 KiB — exercises multi-part assembly
	if err := r.downloadParallelPlain(
		context.Background(), sa, fi, tmpDir,
		map[string]string{"disk.qcow2": want},
		onBytes, smallPartSize, 4,
	); err != nil {
		t.Fatalf("downloadParallelPlain: %v", err)
	}

	got, err := sha256File(filepath.Join(tmpDir, "disk.qcow2"))
	if err != nil {
		t.Fatalf("sha256File: %v", err)
	}
	if got != want {
		t.Errorf("checksum mismatch: got %s, want %s", got, want)
	}
	if heartbeatBytes.Load() == 0 {
		t.Error("expected heartbeat bytes > 0 during parallel download")
	}
}

// TestStageCompressedFileSequentialPath verifies that a zstd-compressed object
// is staged via the sequential path and correctly decompressed.
func TestStageCompressedFileSequentialPath(t *testing.T) {
	t.Parallel()
	raw := bytes.Repeat([]byte("compressed-content"), 100)
	compressed := writeZstd(raw)

	// Stored checksum is of the compressed bytes (what's on storage).
	checksumOfCompressed := sha256hexBytes(compressed)
	meta := buildChecksumsMetadata(t, "myitem", "archive.tar.zst", checksumOfCompressed)

	files := map[string][]byte{
		"staging-test-job/run1/myitem/archive.tar.zst": compressed,
	}
	r, rp := setupStagingTest(t, files, meta)

	tmpDir := t.TempDir()
	if err := r.stageRestorePointItem(
		context.Background(), rp, "myitem", tmpDir, "", 0, 100,
		restoreProgressReporter{},
	); err != nil {
		t.Fatalf("stageRestorePointItem: %v", err)
	}

	// decompressStoredReader strips ".zst" → "archive.tar"
	staged := filepath.Join(tmpDir, "archive.tar")
	got, err := os.ReadFile(staged) // #nosec G304 — test temp dir
	if err != nil {
		t.Fatalf("reading staged file %s: %v", staged, err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("staged content mismatch: got %d bytes, want %d", len(got), len(raw))
	}
}

// TestStageChecksumMismatchFails verifies that a wrong stored checksum causes
// stageRestorePointItem to return an error containing "checksum".
func TestStageChecksumMismatchFails(t *testing.T) {
	t.Parallel()
	data := []byte("some backup data")
	wrongChecksum := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	meta := buildChecksumsMetadata(t, "myitem", "archive.tar", wrongChecksum)

	files := map[string][]byte{
		"staging-test-job/run1/myitem/archive.tar": data,
	}
	r, rp := setupStagingTest(t, files, meta)

	tmpDir := t.TempDir()
	err := r.stageRestorePointItem(
		context.Background(), rp, "myitem", tmpDir, "", 0, 100,
		restoreProgressReporter{},
	)
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error should mention 'checksum', got: %v", err)
	}
}

// TestDownloadRestoreFileRouting verifies the parallel vs sequential predicate
// using a small controllable partSize. This exercises the routing logic
// (objectIsCompressed, size threshold, encryption check) without 64 MiB allocs.
func TestDownloadRestoreFileRouting(t *testing.T) {
	t.Parallel()
	const smallPartSize = 512 // KiB-level threshold for the test

	plain := bytes.Repeat([]byte("X"), 3*smallPartSize) // > 2*512, not compressed
	compressed := writeZstd(plain)

	r, _ := newTestRunner(t)

	tests := []struct {
		name      string
		data      []byte
		expectErr bool // checksum mismatch if wrong path (plain hash ≠ compressed hash)
	}{
		// plain-large: parallel path; checksum of raw bytes is correct.
		{"plain-large", plain, false},
		// compressed-large: sequential path; checksum stored is of compressed bytes.
		// Sequential path hashes storage bytes → matches compressed checksum.
		{"compressed-large", compressed, false},
		// plain-small: sequential path (size < 2*512); checksum of raw bytes.
		{"plain-small", bytes.Repeat([]byte("Y"), 100), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sa := &stagingAdapter{
				files: map[string][]byte{"obj/file.bin": tc.data},
			}
			fi := storage.FileInfo{Path: "obj/file.bin", Size: int64(len(tc.data))}
			// Store checksum of the raw stored bytes (what's on storage).
			wantChecksum := sha256hexBytes(tc.data)
			expected := map[string]string{"file.bin": wantChecksum}

			tmpDir := t.TempDir()
			err := r.downloadRestoreFile(
				context.Background(), sa, fi, tmpDir,
				"" /* no passphrase */, "none" /* no compression */,
				expected,
				func(_ int64) {},
				smallPartSize,
				2,
			)
			if tc.expectErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.expectErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

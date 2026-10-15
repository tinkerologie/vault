//go:build unix

package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/dedup"
)

// recorder collects detail reports for one item.
type recorder struct {
	mu      sync.Mutex
	reports []ProgressDetail
}

func (r *recorder) ctx(parent context.Context, item string) context.Context {
	return WithDetailReporter(parent, func(got string, d ProgressDetail) {
		if got != item {
			return
		}
		r.mu.Lock()
		r.reports = append(r.reports, d)
		r.mu.Unlock()
	})
}

func (r *recorder) last(phase Phase) (ProgressDetail, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.reports) - 1; i >= 0; i-- {
		if r.reports[i].Phase == phase {
			return r.reports[i], true
		}
	}
	return ProgressDetail{}, false
}

func writeTree(t *testing.T, files map[string]int) string {
	t.Helper()
	root := t.TempDir()
	for name, size := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, bytes.Repeat([]byte("v"), size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScanTotals(t *testing.T) {
	root := writeTree(t, map[string]int{"a.txt": 100, "dir/b.bin": 2000, "cache/skip.tmp": 500})
	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	files, size, err := scanTotals(rec.ctx(t.Context(), "f"), "f", root, []string{"cache"}, time.Time{}, nil)
	if err != nil || files != 2 || size != 2100 {
		t.Fatalf("scanTotals = %d files, %d bytes, %v; want 2, 2100 (exclusion and symlink skipped)", files, size, err)
	}
	if d, ok := rec.last(PhaseScanning); !ok || d.FilesDone != 2 || d.BytesTotal != 0 {
		t.Fatalf("scanning report = %+v ok=%v, want growing count without a total", d, ok)
	}

	future := time.Now().Add(time.Hour)
	if f, _, _ := scanTotals(t.Context(), "f", root, nil, future, nil); f != 0 {
		t.Fatalf("changed-since in the future counted %d files", f)
	}
	// A file missing from the previous listing counts despite its old mtime.
	prev := map[string]struct{}{"a.txt": {}}
	if f, b, _ := scanTotals(t.Context(), "f", root, []string{"cache"}, future, prev); f != 1 || b != 2000 {
		t.Fatalf("stale-mtime new file: %d files, %d bytes; want dir/b.bin only", f, b)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := scanTotals(ctx, "f", root, nil, time.Time{}, nil); err == nil {
		t.Fatal("cancelled scan returned no error")
	}
}

func TestItemProgressCountersAndReuse(t *testing.T) {
	var nilCounter *itemProgress
	nilCounter.addBytes(5)
	nilCounter.addFile()

	rec := &recorder{}
	ctx, p := withItemProgress(rec.ctx(t.Context(), "c"), "c", PhaseProcessing, 100, 2)
	p.addBytes(0) // ignored
	if _, err := contextCopy(ctx, &bytes.Buffer{}, bytes.NewReader(make([]byte, 60))); err != nil {
		t.Fatal(err)
	}
	p.addFile()
	if d, _ := rec.last(PhaseProcessing); d.BytesDone != 60 || d.FilesDone != 1 || d.BytesTotal != 100 || d.FilesTotal != 2 {
		t.Fatalf("report = %+v, want 60/100 bytes and 1/2 files", d)
	}

	sized := false
	ctx2, again := ensureItemProgress(ctx, "volume-path", PhaseProcessing, func() (int64, int64) { sized = true; return 1, 1 })
	if again != p || ctx2 != ctx || sized {
		t.Fatal("ensureItemProgress did not reuse the counter on the context")
	}
	_, fresh := ensureItemProgress(t.Context(), "x", PhaseProcessing, nil)
	if fresh == nil || fresh.bytesTotal != 0 {
		t.Fatalf("fresh counter = %+v", fresh)
	}
}

func TestFolderBackupChunkedReportsProgress(t *testing.T) {
	src := writeTree(t, map[string]int{"a.bin": 300_000, "b.bin": 200_000, "c.txt": 10})
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	rec := &recorder{}
	h := &FolderHandler{}
	item := BackupItem{Name: "data", Type: "folder", Settings: map[string]any{"path": src}}
	id, err := h.BackupChunked(rec.ctx(t.Context(), "data"), item, repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := rec.last(PhaseProcessing)
	if !ok || d.BytesTotal != 500_010 || d.BytesDone != 500_010 || d.FilesDone != 3 || d.FilesTotal != 3 {
		t.Fatalf("chunking report = %+v ok=%v, want 500010/500010 bytes and 3/3 files", d, ok)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	// Restore is determinate from the manifest.
	restoreRec := &recorder{}
	if err := h.RestoreChunked(restoreRec.ctx(t.Context(), "data"), item, repo, id, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if d, ok := restoreRec.last(PhaseProcessing); !ok || d.BytesTotal != 500_010 || d.BytesDone != 500_010 || d.FilesDone != 3 {
		t.Fatalf("restore report = %+v ok=%v, want 500010/500010 bytes, 3 files", d, ok)
	}
}

// An incremental chunked backup counts carried-forward files as done, so
// progress reaches its total instead of stalling at the changed fraction.
func TestFolderBackupChunkedCarryForwardCountsAsDone(t *testing.T) {
	src := writeTree(t, map[string]int{"old.bin": 100_000, "new.bin": 1_000})
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(src, "old.bin"), past, past); err != nil {
		t.Fatal(err)
	}
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	h := &FolderHandler{}
	base := BackupItem{Name: "data", Type: "folder", Settings: map[string]any{"path": src}}
	parentID, err := h.BackupChunked(t.Context(), base, repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	parent, err := repo.GetManifest(parentID)
	if err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	inc := BackupItem{Name: "data", Type: "folder", Settings: map[string]any{"path": src, "changed_since": time.Now().Add(-time.Hour).Format(time.RFC3339)}}
	if _, err := h.BackupChunked(rec.ctx(t.Context(), "data"), inc, repo, &parent, nil); err != nil {
		t.Fatal(err)
	}
	if d, _ := rec.last(PhaseProcessing); d.BytesDone != d.BytesTotal || d.FilesDone != 2 {
		t.Fatalf("incremental report = %+v, want done == total with 2 files", d)
	}
}

func TestFolderClassicBackupReportsArchiveProgress(t *testing.T) {
	src := writeTree(t, map[string]int{"a.bin": 70_000, "b/c.bin": 30_000})
	rec := &recorder{}
	h := &FolderHandler{}
	item := BackupItem{Name: "data", Type: "folder", Settings: map[string]any{"path": src}, Compression: "none"}
	if _, err := h.Backup(rec.ctx(t.Context(), "data"), item, t.TempDir(), func(string, int, string) {}); err != nil {
		t.Fatal(err)
	}
	if d, ok := rec.last(PhaseProcessing); !ok || d.BytesTotal != 100_000 || d.BytesDone != 100_000 || d.FilesDone != 2 {
		t.Fatalf("archive report = %+v ok=%v, want 100000/100000 bytes and 2 files", d, ok)
	}
}

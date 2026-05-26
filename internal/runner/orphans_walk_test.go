package runner

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ruaan-deysel/vault/internal/storage"
)

// TestWalkStorageRecursivelyEnumeratesFiles drives the walkStorage helper
// against a real LocalAdapter so that nested directories are descended and
// regular files (not directories) are reported via the callback.
func TestWalkStorageRecursivelyEnumeratesFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Layout:
	//   root/a.txt           (file)
	//   root/sub/b.txt       (file)
	//   root/sub/nested/c.txt (file)
	//   root/empty/          (dir, no files)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("beta-payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "nested", "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	adapter := storage.NewLocalAdapter(root)

	var got []string
	var totalBytes int64
	if err := walkStorage(adapter, "", func(p string, size int64) {
		got = append(got, p)
		totalBytes += size
	}); err != nil {
		t.Fatalf("walkStorage: %v", err)
	}
	sort.Strings(got)
	want := []string{
		"a.txt",
		"sub/b.txt",
		"sub/nested/c.txt",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d files (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("file[%d] = %q, want %q", i, got[i], w)
		}
	}
	// 5 + 12 + 1 = 18 bytes.
	if totalBytes != int64(5+12+1) {
		t.Errorf("totalBytes = %d, want 18", totalBytes)
	}
}

// TestWalkStorageListError exercises the error path: an adapter whose
// initial List() call fails surfaces an error from walkStorage.
type listErrorAdapter struct{ storage.Adapter }

func (l *listErrorAdapter) List(prefix string) ([]storage.FileInfo, error) {
	return nil, os.ErrPermission
}

func TestWalkStorageListError(t *testing.T) {
	t.Parallel()
	a := &listErrorAdapter{}
	err := walkStorage(a, "", func(string, int64) {})
	if err == nil {
		t.Error("expected error when adapter.List() fails")
	}
}

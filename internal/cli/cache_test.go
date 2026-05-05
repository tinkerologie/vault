package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func mountedStub(mounted bool) func(string) bool {
	return func(string) bool { return mounted }
}

func TestCheckCacheMount_NotExist(t *testing.T) {
	t.Parallel()
	status := checkCacheMountWith("/nonexistent/path/that/does/not/exist", mountedStub(false))
	if status != cacheNotExist {
		t.Errorf("got %d, want cacheNotExist", status)
	}
}

func TestCheckCacheMount_DirExistsNotMounted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	if err := os.Mkdir(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	status := checkCacheMountWith(cacheDir, mountedStub(false))
	if status != cacheEmptyNotMounted {
		t.Errorf("got %d, want cacheEmptyNotMounted", status)
	}
}

func TestCheckCacheMount_DirExistsMounted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	if err := os.Mkdir(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Mount-info reports the path as a real mount even though the
	// directory is empty. This used to be misclassified as "unmounted"
	// by the old dir-content heuristic (issue #69).
	status := checkCacheMountWith(cacheDir, mountedStub(true))
	if status != cacheMounted {
		t.Errorf("got %d, want cacheMounted (empty-but-mounted pool)", status)
	}
}

func TestCheckCacheMount_FileNotDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "notadir")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("writefile: %v", err)
	}

	status := checkCacheMountWith(filePath, mountedStub(true))
	if status != cacheNotExist {
		t.Errorf("got %d, want cacheNotExist for plain file", status)
	}
}

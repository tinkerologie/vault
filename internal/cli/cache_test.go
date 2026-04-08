package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckCacheMount_NotExist(t *testing.T) {
	t.Parallel()
	status := checkCacheMount("/nonexistent/path/that/does/not/exist")
	if status != cacheNotExist {
		t.Errorf("got %d, want cacheNotExist", status)
	}
}

func TestCheckCacheMount_EmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	emptyDir := filepath.Join(dir, "cache")
	if err := os.Mkdir(emptyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	status := checkCacheMount(emptyDir)
	if status != cacheEmptyNotMounted {
		t.Errorf("got %d, want cacheEmptyNotMounted", status)
	}
}

func TestCheckCacheMount_PopulatedDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	if err := os.Mkdir(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Add a file to simulate a mounted filesystem.
	if err := os.WriteFile(filepath.Join(cacheDir, "testfile"), []byte("data"), 0o644); err != nil {
		t.Fatalf("writefile: %v", err)
	}

	status := checkCacheMount(cacheDir)
	if status != cacheMounted {
		t.Errorf("got %d, want cacheMounted", status)
	}
}

func TestCheckCacheMount_FileNotDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "notadir")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("writefile: %v", err)
	}

	status := checkCacheMount(filePath)
	if status != cacheNotExist {
		t.Errorf("got %d, want cacheNotExist for plain file", status)
	}
}

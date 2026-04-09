package tempdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateBackupDir(t *testing.T) {
	tests := []struct {
		name string
		dest StorageConfig
	}{
		{
			name: "local storage destination",
			dest: StorageConfig{
				Type:   "local",
				Config: `{"path":"` + t.TempDir() + `"}`,
			},
		},
		{
			name: "non-local storage destination uses system temp",
			dest: StorageConfig{
				Type:   "sftp",
				Config: `{"host":"example.com"}`,
			},
		},
		{
			name: "local with invalid config uses system temp",
			dest: StorageConfig{
				Type:   "local",
				Config: `{invalid`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, cleanup, err := CreateBackupDir(tt.dest, "")
			if err != nil {
				t.Fatalf("CreateBackupDir() error = %v", err)
			}
			defer cleanup()

			if dir == "" {
				t.Fatal("CreateBackupDir() returned empty path")
			}
			if _, err := os.Stat(dir); err != nil {
				t.Fatalf("staging dir %s does not exist: %v", dir, err)
			}

			// Verify the directory contains the appropriate prefix.
			base := filepath.Base(dir)
			if len(base) < 6 { // "backup" prefix minimum
				t.Errorf("unexpected dir name: %s", base)
			}
		})
	}
}

func TestCreateRestoreDir(t *testing.T) {
	dest := StorageConfig{
		Type:   "local",
		Config: `{"path":"` + t.TempDir() + `"}`,
	}

	dir, cleanup, err := CreateRestoreDir(dest, "")
	if err != nil {
		t.Fatalf("CreateRestoreDir() error = %v", err)
	}
	defer cleanup()

	if dir == "" {
		t.Fatal("CreateRestoreDir() returned empty path")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("staging dir %s does not exist: %v", dir, err)
	}
}

func TestCreateBackupDirLocalStaging(t *testing.T) {
	localPath := t.TempDir()
	dest := StorageConfig{
		Type:   "local",
		Config: `{"path":"` + localPath + `"}`,
	}

	// Override cache paths so the cache cascade doesn't match.
	restorePaths := SetCachePathsForTest([]string{"/nonexistent-cache-path"})
	defer restorePaths()

	dir, cleanup, err := CreateBackupDir(dest, "")
	if err != nil {
		t.Fatalf("CreateBackupDir() error = %v", err)
	}
	defer cleanup()

	// Should have created under <localPath>/.vault-stage/
	stageBase := filepath.Join(localPath, StageDirName)
	rel, err := filepath.Rel(stageBase, dir)
	if err != nil {
		t.Fatalf("unexpected: dir %s not relative to %s: %v", dir, stageBase, err)
	}
	if filepath.IsAbs(rel) || len(rel) >= 2 && rel[:2] == ".." {
		t.Errorf("expected dir under %s, got %s", stageBase, dir)
	}
}

func TestCreateBackupDirWithOverride(t *testing.T) {
	overridePath := t.TempDir()
	dest := StorageConfig{Type: "sftp", Config: `{}`}

	dir, cleanup, err := CreateBackupDir(dest, overridePath)
	if err != nil {
		t.Fatalf("CreateBackupDir() error = %v", err)
	}
	defer cleanup()

	stageBase := filepath.Join(overridePath, StageDirName)
	rel, err := filepath.Rel(stageBase, dir)
	if err != nil {
		t.Fatalf("dir %s not relative to %s: %v", dir, stageBase, err)
	}
	if filepath.IsAbs(rel) || (len(rel) >= 2 && rel[:2] == "..") {
		t.Errorf("expected dir under %s, got %s", stageBase, dir)
	}
}

func TestCleanupFunc(t *testing.T) {
	tmpBase := filepath.Join(t.TempDir(), StageDirName)
	if err := os.MkdirAll(tmpBase, 0750); err != nil {
		t.Fatal(err)
	}

	dir, err := os.MkdirTemp(tmpBase, "backup-*")
	if err != nil {
		t.Fatal(err)
	}

	// Write a file inside to verify RemoveAll works.
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	fn := cleanupFunc(dir, tmpBase)
	fn()

	// Dir should be gone.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected dir %s to be removed", dir)
	}

	// Parent .vault-stage should be pruned (it's empty).
	if _, err := os.Stat(tmpBase); !os.IsNotExist(err) {
		t.Errorf("expected empty parent %s to be pruned", tmpBase)
	}
}

func TestCleanupFuncPreservesNonEmpty(t *testing.T) {
	tmpBase := filepath.Join(t.TempDir(), StageDirName)
	if err := os.MkdirAll(tmpBase, 0750); err != nil {
		t.Fatal(err)
	}

	dir1, _ := os.MkdirTemp(tmpBase, "backup-*")
	dir2, _ := os.MkdirTemp(tmpBase, "backup-*")

	// Clean up only dir1; dir2 still exists.
	fn := cleanupFunc(dir1, tmpBase)
	fn()

	if _, err := os.Stat(dir1); !os.IsNotExist(err) {
		t.Errorf("expected dir1 %s to be removed", dir1)
	}

	// Parent should NOT be pruned because dir2 still exists.
	if _, err := os.Stat(tmpBase); err != nil {
		t.Errorf("parent %s should still exist (dir2 present): %v", tmpBase, err)
	}

	// Cleanup dir2 as well.
	os.RemoveAll(dir2)
}

func TestCleanupStale(t *testing.T) {
	localPath := t.TempDir()
	stageBase := filepath.Join(localPath, StageDirName)
	if err := os.MkdirAll(stageBase, 0750); err != nil {
		t.Fatal(err)
	}

	// Create some "stale" directories.
	stale1, _ := os.MkdirTemp(stageBase, "backup-*")
	stale2, _ := os.MkdirTemp(stageBase, "restore-*")

	// Write files in one to verify RemoveAll works.
	os.WriteFile(filepath.Join(stale1, "file.tar"), []byte("data"), 0644)

	// Override cache paths to avoid scanning real system paths.
	restorePaths := SetCachePathsForTest([]string{"/nonexistent-cache-path"})
	defer restorePaths()

	dests := []StorageConfig{
		{Type: "local", Config: `{"path":"` + localPath + `"}`},
	}

	CleanupStale(dests)

	// Both dirs should be removed.
	if _, err := os.Stat(stale1); !os.IsNotExist(err) {
		t.Errorf("stale dir %s should have been cleaned", stale1)
	}
	if _, err := os.Stat(stale2); !os.IsNotExist(err) {
		t.Errorf("stale dir %s should have been cleaned", stale2)
	}

	// Parent .vault-stage should be pruned.
	if _, err := os.Stat(stageBase); !os.IsNotExist(err) {
		t.Errorf("empty stage base %s should have been pruned", stageBase)
	}
}

func TestCleanupStaleLegacy(t *testing.T) {
	tmpRoot := t.TempDir()

	// Create a legacy .vault-tmp directory under a "cache" path.
	legacyBase := filepath.Join(tmpRoot, ".vault-tmp")
	if err := os.MkdirAll(legacyBase, 0750); err != nil {
		t.Fatal(err)
	}
	stale, _ := os.MkdirTemp(legacyBase, "backup-*")
	os.WriteFile(filepath.Join(stale, "data.gz"), []byte("old"), 0644)

	// Override cache paths so it hits our tmpRoot.
	restorePaths := SetCachePathsForTest([]string{tmpRoot})
	defer restorePaths()

	CleanupStale(nil)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("legacy stale dir %s should have been cleaned", stale)
	}
}

func TestCleanupStaleNoOp(t *testing.T) {
	// Should not panic on empty input.
	restorePaths := SetCachePathsForTest([]string{"/nonexistent-cache-path"})
	defer restorePaths()

	CleanupStale(nil)
	CleanupStale([]StorageConfig{})
}

func TestResolveInfo(t *testing.T) {
	localPath := t.TempDir()
	dests := []StorageConfig{
		{Type: "local", Config: `{"path":"` + localPath + `"}`},
	}

	info := ResolveInfo(dests, "")
	if info.ResolvedPath == "" {
		t.Fatal("ResolvedPath should not be empty")
	}
	if info.Source == "" {
		t.Fatal("Source should not be empty")
	}
	if info.DiskTotalBytes == 0 {
		t.Fatal("DiskTotalBytes should be non-zero")
	}
	if len(info.Cascade) == 0 {
		t.Fatal("Cascade should not be empty")
	}
}

func TestResolveInfoWithOverride(t *testing.T) {
	overridePath := t.TempDir()
	info := ResolveInfo(nil, overridePath)
	if info.Source != "override" {
		t.Errorf("Source = %q, want %q", info.Source, "override")
	}
	if info.ResolvedPath != overridePath {
		t.Errorf("ResolvedPath = %q, want %q", info.ResolvedPath, overridePath)
	}
}

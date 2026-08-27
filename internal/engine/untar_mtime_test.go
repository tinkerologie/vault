package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildMtimeTar constructs a tar archive with explicit, distinct modification
// times for nested directories, regular files, hard links, and symlinks.
func buildMtimeTar(t *testing.T, parentDirTime, subDirTime, fileTime, linkTime time.Time) string {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// 1. Parent directory
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "parent/",
		Mode:     0o755,
		ModTime:  parentDirTime,
	}); err != nil {
		t.Fatalf("WriteHeader(parent dir): %v", err)
	}

	// 2. Child subdirectory inside parent
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "parent/child/",
		Mode:     0o755,
		ModTime:  subDirTime,
	}); err != nil {
		t.Fatalf("WriteHeader(child dir): %v", err)
	}

	// 3. Regular file inside parent/child
	payload := []byte("mtime preservation payload")
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "parent/child/data.txt",
		Size:     int64(len(payload)),
		Mode:     0o644,
		ModTime:  fileTime,
	}); err != nil {
		t.Fatalf("WriteHeader(data.txt): %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("Write(data.txt): %v", err)
	}

	// 4. Another regular file to be hardlinked
	linkPayload := []byte("hardlink payload")
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "parent/child/link_src.txt",
		Size:     int64(len(linkPayload)),
		Mode:     0o644,
		ModTime:  linkTime,
	}); err != nil {
		t.Fatalf("WriteHeader(link_src.txt): %v", err)
	}
	if _, err := tw.Write(linkPayload); err != nil {
		t.Fatalf("Write(link_src.txt): %v", err)
	}

	// 5. Hard link to link_src.txt
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeLink,
		Name:     "parent/child/hard.txt",
		Linkname: "parent/child/link_src.txt",
		Mode:     0o644,
		ModTime:  linkTime,
	}); err != nil {
		t.Fatalf("WriteHeader(hard.txt): %v", err)
	}

	// 6. Symlink to data.txt
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "parent/child/sym.txt",
		Linkname: "data.txt",
		Mode:     0o777,
		ModTime:  linkTime,
	}); err != nil {
		t.Fatalf("WriteHeader(sym.txt): %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}

	archivePath := filepath.Join(t.TempDir(), "mtime_test.tar")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile archive: %v", err)
	}
	return archivePath
}

// TestUntarDirectory_PreservesModTimes verifies that classic tar extraction
// applies the archive header ModTime to directories, regular files, and hardlinks
// (closing the parity gap with the dedup restore path, issue #354).
func TestUntarDirectory_PreservesModTimes(t *testing.T) {
	parentDirTime := time.Date(2021, 3, 15, 10, 30, 0, 0, time.UTC)
	subDirTime := time.Date(2022, 6, 20, 14, 45, 0, 0, time.UTC)
	fileTime := time.Date(2020, 1, 1, 8, 0, 0, 0, time.UTC)
	linkTime := time.Date(2023, 9, 10, 16, 20, 0, 0, time.UTC)

	archive := buildMtimeTar(t, parentDirTime, subDirTime, fileTime, linkTime)
	dest := t.TempDir()

	if err := untarDirectory(context.Background(), archive, dest); err != nil {
		t.Fatalf("untarDirectory failed: %v", err)
	}

	// Check regular file mtime
	filePath := filepath.Join(dest, "parent", "child", "data.txt")
	fi, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", filePath, err)
	}
	if got, want := fi.ModTime().Truncate(time.Second), fileTime.Truncate(time.Second); !got.Equal(want) {
		t.Errorf("regular file mtime = %v, want %v", got, want)
	}

	// Check hardlink mtime
	hardPath := filepath.Join(dest, "parent", "child", "hard.txt")
	hfi, err := os.Stat(hardPath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", hardPath, err)
	}
	if got, want := hfi.ModTime().Truncate(time.Second), linkTime.Truncate(time.Second); !got.Equal(want) {
		t.Errorf("hard link mtime = %v, want %v", got, want)
	}

	// Check child directory mtime
	childPath := filepath.Join(dest, "parent", "child")
	cfi, err := os.Stat(childPath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", childPath, err)
	}
	if got, want := cfi.ModTime().Truncate(time.Second), subDirTime.Truncate(time.Second); !got.Equal(want) {
		t.Errorf("child dir mtime = %v, want %v", got, want)
	}

	// Check parent directory mtime (must not be clobbered by child extractions)
	parentPath := filepath.Join(dest, "parent")
	pfi, err := os.Stat(parentPath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", parentPath, err)
	}
	if got, want := pfi.ModTime().Truncate(time.Second), parentDirTime.Truncate(time.Second); !got.Equal(want) {
		t.Errorf("parent dir mtime = %v, want %v", got, want)
	}

	// Verify symlink was created successfully
	symPath := filepath.Join(dest, "parent", "child", "sym.txt")
	target, err := os.Readlink(symPath)
	if err != nil || target != "data.txt" {
		t.Errorf("Readlink(%s) = %q, err = %v, want %q", symPath, target, err, "data.txt")
	}
}

// TestUntarDirectoryFiltered_PreservesModTimes verifies that partial/filtered
// extraction also preserves mtimes for selected files and directories.
func TestUntarDirectoryFiltered_PreservesModTimes(t *testing.T) {
	parentDirTime := time.Date(2021, 3, 15, 10, 30, 0, 0, time.UTC)
	subDirTime := time.Date(2022, 6, 20, 14, 45, 0, 0, time.UTC)
	fileTime := time.Date(2020, 1, 1, 8, 0, 0, 0, time.UTC)
	linkTime := time.Date(2023, 9, 10, 16, 20, 0, 0, time.UTC)

	archive := buildMtimeTar(t, parentDirTime, subDirTime, fileTime, linkTime)
	dest := t.TempDir()

	// Only include the child directory and its descendants
	if err := untarDirectoryFiltered(context.Background(), archive, dest, []string{"parent/child"}); err != nil {
		t.Fatalf("untarDirectoryFiltered failed: %v", err)
	}

	filePath := filepath.Join(dest, "parent", "child", "data.txt")
	fi, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", filePath, err)
	}
	if got, want := fi.ModTime().Truncate(time.Second), fileTime.Truncate(time.Second); !got.Equal(want) {
		t.Errorf("filtered regular file mtime = %v, want %v", got, want)
	}

	childPath := filepath.Join(dest, "parent", "child")
	cfi, err := os.Stat(childPath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", childPath, err)
	}
	if got, want := cfi.ModTime().Truncate(time.Second), subDirTime.Truncate(time.Second); !got.Equal(want) {
		t.Errorf("filtered child dir mtime = %v, want %v", got, want)
	}
}

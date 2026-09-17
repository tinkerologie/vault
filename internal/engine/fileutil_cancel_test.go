package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestCopyFileWithProgress_Cancelled pins the #171 contract: a cancelled run
// context aborts a file copy promptly instead of running to completion.
func TestCopyFileWithProgress_Cancelled(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	// 4 MiB source → four 1 MiB chunks; cancel fires after the first chunk.
	if err := os.WriteFile(src, make([]byte, 4<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	// t.TempDir() lands under an allowed restore root on both dev (macOS:
	// /var/folders/…) and CI (Linux: /tmp), keeping the test hermetic.
	dst := filepath.Join(t.TempDir(), "vault-test-cancel.img")

	ctx, cancel := context.WithCancel(context.Background())
	err := copyFileWithProgress(ctx, src, dst, func(copied int64) {
		if copied >= 1<<20 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if fi, statErr := os.Stat(dst); statErr == nil && fi.Size() >= 4<<20 {
		t.Fatal("copy ran to completion despite cancellation")
	}
}

func TestCopyFileWithProgress_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	if err := os.WriteFile(src, []byte("test-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Rejects symlinked destination
	target := filepath.Join(dir, "target.img")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkDst := filepath.Join(dir, "link.img")
	if err := os.Symlink(target, symlinkDst); err != nil {
		t.Skipf("skipping: symlinks not supported on this filesystem: %v", err)
	}
	err := copyFile(context.Background(), src, symlinkDst)
	if err == nil {
		t.Fatal("expected error copying through symlink destination, got nil")
	}

	// 2. Rejects symlinked parent directory
	parentReal := filepath.Join(dir, "real_dir")
	if err := os.MkdirAll(parentReal, 0o755); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(dir, "link_dir")
	if err := os.Symlink(parentReal, parentLink); err != nil {
		t.Skipf("skipping: symlinks not supported on this filesystem: %v", err)
	}
	err = copyFile(context.Background(), src, filepath.Join(parentLink, "file.img"))
	if err == nil {
		t.Fatal("expected error copying through symlinked parent directory, got nil")
	}

	// 3. Rejects symlinked intermediate ancestor directory
	subDir := filepath.Join(parentLink, "nested", "deeper")
	err = copyFile(context.Background(), src, filepath.Join(subDir, "file.img"))
	if err == nil {
		t.Fatal("expected error copying through symlinked intermediate ancestor directory, got nil")
	}
}

func TestOpenRestoreDestination_Branches(t *testing.T) {
	dir := t.TempDir()

	// 1. Success on normal file beneath allowed root
	target := filepath.Join(dir, "sub", "test.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := openRestoreDestination(target, target, 0o644)
	if err != nil {
		t.Fatalf("openRestoreDestination(%q) failed: %v", target, err)
	}
	_ = f.Close()

	// 2. Direct child under root
	rootChild := filepath.Join(t.TempDir(), "child.txt")
	f2, err := openRestoreDestination(rootChild, rootChild, 0o644)
	if err != nil {
		t.Fatalf("openRestoreDestination(%q) failed: %v", rootChild, err)
	}
	_ = f2.Close()

	// 3. Fallback when unapproved root
	unapproved := "/unapproved/path/test.txt"
	_, err = openRestoreDestination(unapproved, unapproved, 0o644)
	if err == nil {
		t.Fatal("expected error opening file under unapproved root")
	}

	// 4. Invalid relative traversal path
	_, err = openRestoreDestination("/tmp/../outside/test.txt", "/tmp/../outside/test.txt", 0o644)
	if err == nil {
		t.Fatal("expected error with traversal in path")
	}

	// 5. Non-existent parent directory
	missingParent := filepath.Join(dir, "missing_parent_dir", "test.txt")
	_, err = openRestoreDestination(missingParent, missingParent, 0o644)
	if err == nil {
		t.Fatal("expected error when parent directory does not exist")
	}

	// 6. Dot and redundant slashes in path
	dotPath := filepath.Join(dir, "sub") + "/./dot.txt"
	f3, err := openRestoreDestination(dotPath, dotPath, 0o644)
	if err != nil {
		t.Fatalf("openRestoreDestination with dot failed: %v", err)
	}
	_ = f3.Close()

	// 7. Relative dst with absolute normalizedDst
	f4, err := openRestoreDestination("relative/dot.txt", dotPath, 0o644)
	if err != nil {
		t.Fatalf("openRestoreDestination with relative dst failed: %v", err)
	}
	_ = f4.Close()

	// 8. Intermediate directory is a symlink
	realSubDir := filepath.Join(dir, "real_sub")
	if err := os.MkdirAll(realSubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symSubDir := filepath.Join(dir, "sym_sub")
	if err := os.Symlink(realSubDir, symSubDir); err == nil {
		targetInSym := filepath.Join(symSubDir, "test.txt")
		_, err = openRestoreDestination(targetInSym, targetInSym, 0o644)
		if err == nil {
			t.Fatal("expected error traversing intermediate symlink directory")
		}
	}

	// 9. Destination leaf itself is a symlink
	realFile := filepath.Join(dir, "real_file.txt")
	if err := os.WriteFile(realFile, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	symFile := filepath.Join(dir, "sym_file.txt")
	if err := os.Symlink(realFile, symFile); err == nil {
		_, err = openRestoreDestination(symFile, symFile, 0o644)
		if err == nil {
			t.Fatal("expected error opening destination leaf symlink")
		}

		// copyFile destination is a symlink
		if err := copyFile(context.Background(), realFile, symFile); err == nil {
			t.Fatal("expected error copying file into symlink destination")
		}
	}

	// 10. copyFile rejects unsafe destinations
	if err := copyFile(context.Background(), realFile, "../escape"); err == nil {
		t.Fatal("expected error copying file to ../escape")
	}
	if err := copyFile(context.Background(), realFile, "..\\escape"); err == nil {
		t.Fatal("expected error copying file to ..\\escape")
	}

	// 11. Destination leaf is an existing directory
	existingDirLeaf := filepath.Join(dir, "sub", "existing_dir_leaf")
	if err := os.MkdirAll(existingDirLeaf, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openRestoreDestination(existingDirLeaf, existingDirLeaf, 0o644); err == nil {
		t.Fatal("expected error opening destination when leaf is an existing directory")
	}
}

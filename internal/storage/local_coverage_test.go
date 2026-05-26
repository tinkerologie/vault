package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLocalWrite_ErrorBranches drives the error paths in Write that the
// happy-path test doesn't reach: bad relative path (traversal handled
// already, so we use a permission-denied parent), and reader that errors
// mid-copy.
func TestLocalWrite_ErrorBranches(t *testing.T) {
	t.Parallel()

	t.Run("reader that errors mid-copy", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		a := NewLocalAdapter(dir)
		errReader := &errOnRead{after: 4, err: io.ErrUnexpectedEOF, data: []byte("abcdEFGH")}
		err := a.Write("partial.bin", errReader)
		if err == nil {
			t.Fatal("expected Write to fail when reader errors mid-copy")
		}
		// Confirm no partial file is left behind at the destination.
		if _, statErr := os.Stat(filepath.Join(dir, "partial.bin")); !os.IsNotExist(statErr) {
			t.Errorf("expected no partial file at destination, got err=%v", statErr)
		}
	})

	t.Run("write to read-only parent dir fails", func(t *testing.T) {
		// CreateTemp inside a read-only parent will fail. Skip on root since
		// permission bits don't restrict root.
		if runtime.GOOS == "windows" {
			t.Skip("POSIX permission bits not enforced the same way on Windows")
		}
		if os.Geteuid() == 0 {
			t.Skip("root bypasses POSIX permission checks")
		}
		t.Parallel()
		dir := t.TempDir()
		// Pre-create the destination parent directory and make it read-only.
		// MkdirAll on an existing dir is a no-op, so subsequent CreateTemp
		// inside it should fail with EACCES.
		ro := filepath.Join(dir, "ro")
		if err := os.MkdirAll(ro, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Chmod(ro, 0o500); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })

		a := NewLocalAdapter(dir)
		if err := a.Write("ro/file.bin", bytes.NewReader([]byte("hi"))); err == nil {
			t.Fatal("expected Write to fail when parent dir is read-only")
		}
	})

	t.Run("invalid path rejected", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		a := NewLocalAdapter(dir)
		// "" is forbidden by safepath.JoinUnderBase with allowRoot=false.
		if err := a.Write("", bytes.NewReader([]byte("x"))); err == nil {
			t.Fatal("expected error for empty path")
		}
	})
}

// TestLocalReadRange_ErrorBranches drives error branches in ReadRange:
// invalid path, missing file, and negative parameters.
func TestLocalReadRange_ErrorBranches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := NewLocalAdapter(dir)

	t.Run("invalid path rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := a.ReadRange("", 0, 1); err == nil {
			t.Fatal("expected error for empty path")
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		t.Parallel()
		if _, err := a.ReadRange("does/not/exist.bin", 0, 1); err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("negative offset rejected", func(t *testing.T) {
		t.Parallel()
		if err := a.Write("neg.bin", bytes.NewReader([]byte("123456"))); err != nil {
			t.Fatalf("Write setup: %v", err)
		}
		if _, err := a.ReadRange("neg.bin", -1, 5); err == nil {
			t.Fatal("expected error for negative offset")
		}
	})

	t.Run("negative length rejected", func(t *testing.T) {
		t.Parallel()
		if err := a.Write("neg2.bin", bytes.NewReader([]byte("123456"))); err != nil {
			t.Fatalf("Write setup: %v", err)
		}
		if _, err := a.ReadRange("neg2.bin", 0, -1); err == nil {
			t.Fatal("expected error for negative length")
		}
	})
}

// TestLocalList_ErrorBranches drives the missing-prefix path through List.
func TestLocalList_ErrorBranches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := NewLocalAdapter(dir)

	if _, err := a.List("missing-prefix"); err == nil {
		t.Fatal("expected error listing a missing prefix")
	}
}

// TestLocalStat_ErrorBranches covers Stat's invalid-path and missing-file
// branches.
func TestLocalStat_ErrorBranches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := NewLocalAdapter(dir)

	if _, err := a.Stat(""); err == nil {
		t.Fatal("expected error for empty path")
	}
	if _, err := a.Stat("does/not/exist.bin"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestLocalDelete_InvalidPath covers Delete's invalid-path branch.
func TestLocalDelete_InvalidPath(t *testing.T) {
	t.Parallel()
	a := NewLocalAdapter(t.TempDir())
	if err := a.Delete(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

// TestLocalTestConnection_NotDirectory exercises the "is not a directory"
// branch when basePath is a file.
func TestLocalTestConnection_NotDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "afile")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	a := NewLocalAdapter(filePath)
	if err := a.TestConnection(); err == nil {
		t.Fatal("expected error when basePath is a file, not a directory")
	}
}

// TestLocalTestConnection_NotWritable exercises the "not writable" branch
// by pointing basePath at a read-only directory.
func TestLocalTestConnection_NotWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses POSIX permission checks")
	}
	t.Parallel()
	dir := t.TempDir()
	ro := filepath.Join(dir, "ro")
	if err := os.MkdirAll(ro, 0o500); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })

	a := NewLocalAdapter(ro)
	if err := a.TestConnection(); err == nil {
		t.Fatal("expected TestConnection to fail on a read-only basePath")
	}
}

// TestLocalGetCapacity_BadBasePath covers the statfs error branch when
// basePath does not exist.
func TestLocalGetCapacity_BadBasePath(t *testing.T) {
	t.Parallel()
	a := NewLocalAdapter("/does/not/exist/vault-cap-test")
	if _, err := a.GetCapacity(context.Background()); err == nil {
		t.Fatal("expected statfs error for missing basePath")
	}
}

// errOnRead returns `data` then errors after `after` bytes.
type errOnRead struct {
	after int
	read  int
	data  []byte
	err   error
}

func (e *errOnRead) Read(p []byte) (int, error) {
	if e.read >= e.after {
		return 0, e.err
	}
	remaining := e.after - e.read
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > len(e.data)-e.read {
		remaining = len(e.data) - e.read
	}
	if remaining <= 0 {
		return 0, e.err
	}
	copy(p, e.data[e.read:e.read+remaining])
	e.read += remaining
	if e.read >= e.after {
		return remaining, e.err
	}
	return remaining, nil
}

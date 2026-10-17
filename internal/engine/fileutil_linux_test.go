//go:build linux

package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCopyFileKernelPath copies a multi-MiB file within one directory, where
// Linux can serve it with copy_file_range. CI runs this test under strace and
// fails unless a successful copy_file_range call appears (issue #500).
func TestCopyFileKernelPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.img")
	data := bytes.Repeat([]byte("vault-kernel-copy"), (8<<20)/17)
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.img")
	if err := copyFile(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("copied content differs from source")
	}
}

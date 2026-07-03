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
	dst := filepath.Join("/tmp", "vault-test-cancel.img")
	t.Cleanup(func() { _ = os.Remove(dst) })

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

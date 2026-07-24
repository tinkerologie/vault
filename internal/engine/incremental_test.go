package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPathChangedSinceFileAndDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	unchangedDir := filepath.Join(root, "unchanged")
	changedDir := filepath.Join(root, "changed")
	if err := os.MkdirAll(unchangedDir, 0755); err != nil {
		t.Fatalf("MkdirAll unchangedDir: %v", err)
	}
	if err := os.MkdirAll(changedDir, 0755); err != nil {
		t.Fatalf("MkdirAll changedDir: %v", err)
	}

	oldFile := filepath.Join(unchangedDir, "old.txt")
	newFile := filepath.Join(changedDir, "new.txt")
	if err := os.WriteFile(oldFile, []byte("old"), 0644); err != nil {
		t.Fatalf("WriteFile old: %v", err)
	}
	if err := os.WriteFile(newFile, []byte("new"), 0644); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}

	reference := time.Now()
	oldTime := reference.Add(-2 * time.Hour)
	newTime := reference.Add(2 * time.Hour)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes old: %v", err)
	}
	if err := os.Chtimes(newFile, newTime, newTime); err != nil {
		t.Fatalf("Chtimes new: %v", err)
	}

	changed, err := pathChangedSince(context.Background(), oldFile, reference)
	if err != nil {
		t.Fatalf("pathChangedSince(oldFile) error = %v", err)
	}
	if changed {
		t.Fatal("expected unchanged file to be skipped")
	}

	changed, err = pathChangedSince(context.Background(), newFile, reference)
	if err != nil {
		t.Fatalf("pathChangedSince(newFile) error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed file to be detected")
	}

	changed, err = pathChangedSince(context.Background(), unchangedDir, reference)
	if err != nil {
		t.Fatalf("pathChangedSince(unchangedDir) error = %v", err)
	}
	if changed {
		t.Fatal("expected unchanged directory to be skipped")
	}

	changed, err = pathChangedSince(context.Background(), changedDir, reference)
	if err != nil {
		t.Fatalf("pathChangedSince(changedDir) error = %v", err)
	}
	if !changed {
		t.Fatal("expected changed directory to be detected")
	}
}

func TestFilterChangedDomainDisksPreservesIndexes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	firstDisk := filepath.Join(root, "disk0.qcow2")
	secondDisk := filepath.Join(root, "disk1.qcow2")
	if err := os.WriteFile(firstDisk, []byte("first"), 0644); err != nil {
		t.Fatalf("WriteFile firstDisk: %v", err)
	}
	if err := os.WriteFile(secondDisk, []byte("second"), 0644); err != nil {
		t.Fatalf("WriteFile secondDisk: %v", err)
	}

	reference := time.Now()
	oldTime := reference.Add(-2 * time.Hour)
	newTime := reference.Add(2 * time.Hour)
	if err := os.Chtimes(firstDisk, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes firstDisk: %v", err)
	}
	if err := os.Chtimes(secondDisk, newTime, newTime); err != nil {
		t.Fatalf("Chtimes secondDisk: %v", err)
	}

	disks := []domainDisk{{Index: 0, Path: firstDisk, Target: "vda"}, {Index: 1, Path: secondDisk, Target: "vdb"}}
	changed, err := filterChangedDomainDisks(context.Background(), disks, reference)
	if err != nil {
		t.Fatalf("filterChangedDomainDisks() error = %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed disk, got %d", len(changed))
	}
	if changed[0].Index != 1 || changed[0].Target != "vdb" {
		t.Fatalf("unexpected changed disk: %+v", changed[0])
	}
}

// nthErrCancelCtx is a context whose Err() reports cancellation only from the
// Nth call onward, letting a test target a specific ctx.Err() check inside
// pathChangedSince deterministically (filepath.Walk itself ignores the
// context, so Err() is the only observation point).
type nthErrCancelCtx struct {
	context.Context
	calls    int
	cancelAt int // 1-based: Err() returns context.Canceled on call >= cancelAt
}

func (c *nthErrCancelCtx) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return c.Context.Err()
}

// TestPathChangedSinceHonoursCancellation verifies that cancellation is
// honoured both before the walk starts (top-level guard, Err() call #1) and
// during the walk (per-file callback guard, Err() call #2 on the root entry),
// so a large unchanged tree cannot block backup cancellation (issue #251).
// The second case fails if the in-callback guard is removed: the walk would
// then run to completion and return (false, nil) instead of context.Canceled.
func TestPathChangedSinceHonoursCancellation(t *testing.T) {
	t.Parallel()

	// A directory whose contents predate the reference time, so without a
	// cancellation guard pathChangedSince would walk the whole tree.
	dir := t.TempDir()
	old := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(old, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	reference := time.Now()
	past := reference.Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if err := os.Chtimes(dir, past, past); err != nil {
		t.Fatalf("Chtimes dir: %v", err)
	}

	cases := []struct {
		name     string
		cancelAt int
	}{
		{name: "pre-walk guard", cancelAt: 1},
		{name: "in-walk callback guard", cancelAt: 2},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := &nthErrCancelCtx{Context: context.Background(), cancelAt: tc.cancelAt}
			if _, err := pathChangedSince(ctx, dir, reference); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled, got %v", err)
			}
		})
	}
}

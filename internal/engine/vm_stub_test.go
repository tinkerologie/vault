//go:build !linux

package engine

import (
	"context"
	"testing"
)

// VM stubs return errors on non-Linux platforms. Close and DeleteCheckpoint
// are no-ops because callers may invoke them from cleanup paths regardless
// of platform.
//
// NewVMHandler itself is already covered by TestNewVMHandlerPlatform in
// vm_test.go (which compiles on all platforms), so we don't repeat it here.

func TestVMHandlerListItemsStubReturnsError(t *testing.T) {
	t.Parallel()

	h := &VMHandler{}
	items, err := h.ListItems()
	if err == nil {
		t.Fatal("expected platform-not-supported error from VMHandler.ListItems on non-Linux")
	}
	if items != nil {
		t.Fatalf("expected nil items from stub, got %#v", items)
	}
}

func TestVMHandlerBackupStubReturnsError(t *testing.T) {
	t.Parallel()

	h := &VMHandler{}
	result, err := h.Backup(context.Background(), BackupItem{Name: "vm1"}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected platform-not-supported error from VMHandler.Backup on non-Linux")
	}
	if result != nil {
		t.Fatalf("expected nil result from stub, got %#v", result)
	}
}

func TestVMHandlerRestoreStubReturnsError(t *testing.T) {
	t.Parallel()

	h := &VMHandler{}
	err := h.Restore(context.Background(), BackupItem{Name: "vm1"}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected platform-not-supported error from VMHandler.Restore on non-Linux")
	}
}

func TestVMHandlerCloseStubIsNoop(t *testing.T) {
	t.Parallel()

	h := &VMHandler{}
	if err := h.Close(); err != nil {
		t.Fatalf("Close stub should be a no-op, got %v", err)
	}
}

func TestVMHandlerDeleteCheckpointStubIsNoop(t *testing.T) {
	t.Parallel()

	h := &VMHandler{}
	if err := h.DeleteCheckpoint("vm1", "cp1"); err != nil {
		t.Fatalf("DeleteCheckpoint stub should be a no-op, got %v", err)
	}
}

//go:build !linux

package engine

import (
	"context"
	"testing"
)

// ZFS stubs return errors (or empty slices) on non-Linux platforms. The
// list methods specifically return empty slices rather than errors so the
// UI can render an empty pool picker without surfacing an error toast.

func TestNewZFSHandlerStubReturnsError(t *testing.T) {
	t.Parallel()

	h, err := NewZFSHandler()
	if err == nil {
		t.Fatal("expected platform-not-supported error from NewZFSHandler on non-Linux")
	}
	if h != nil {
		t.Fatalf("expected nil handler from stub, got %#v", h)
	}
}

func TestZFSHandlerListItemsStubReturnsError(t *testing.T) {
	t.Parallel()

	h := &ZFSHandler{}
	items, err := h.ListItems()
	if err == nil {
		t.Fatal("expected platform-not-supported error from ZFSHandler.ListItems on non-Linux")
	}
	if items != nil {
		t.Fatalf("expected nil items from stub, got %#v", items)
	}
}

func TestZFSHandlerBackupStubReturnsError(t *testing.T) {
	t.Parallel()

	h := &ZFSHandler{}
	result, err := h.Backup(context.Background(), BackupItem{Name: "tank"}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected platform-not-supported error from ZFSHandler.Backup on non-Linux")
	}
	if result != nil {
		t.Fatalf("expected nil result from stub, got %#v", result)
	}
}

func TestZFSHandlerRestoreStubReturnsError(t *testing.T) {
	t.Parallel()

	h := &ZFSHandler{}
	err := h.Restore(context.Background(), BackupItem{Name: "tank"}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected platform-not-supported error from ZFSHandler.Restore on non-Linux")
	}
}

func TestZFSHandlerListNVMePoolsStubReturnsEmpty(t *testing.T) {
	t.Parallel()

	h := &ZFSHandler{}
	pools, err := h.ListNVMePools()
	if err != nil {
		t.Fatalf("ListNVMePools stub should not error, got %v", err)
	}
	if len(pools) != 0 {
		t.Fatalf("expected empty pool slice from stub, got %#v", pools)
	}
}

func TestZFSHandlerListZFSMountpointsStubReturnsEmpty(t *testing.T) {
	t.Parallel()

	h := &ZFSHandler{}
	pools, err := h.ListZFSMountpoints()
	if err != nil {
		t.Fatalf("ListZFSMountpoints stub should not error, got %v", err)
	}
	if len(pools) != 0 {
		t.Fatalf("expected empty mountpoint slice from stub, got %#v", pools)
	}
}

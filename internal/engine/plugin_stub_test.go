//go:build !linux

package engine

import (
	"context"
	"testing"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// Plugin stubs all return errors on non-Linux platforms. These tests pin
// that contract — if the package is ever compiled on macOS, every entry
// point must fail loudly rather than silently no-op.

func TestNewPluginHandlerStubReturnsError(t *testing.T) {
	t.Parallel()

	h, err := NewPluginHandler()
	if err == nil {
		t.Fatal("expected platform-not-supported error from NewPluginHandler on non-Linux")
	}
	if h != nil {
		t.Fatalf("expected nil handler from stub, got %#v", h)
	}
}

func TestPluginHandlerListItemsStubReturnsError(t *testing.T) {
	t.Parallel()

	h := &PluginHandler{}
	items, err := h.ListItems()
	if err == nil {
		t.Fatal("expected platform-not-supported error from PluginHandler.ListItems on non-Linux")
	}
	if items != nil {
		t.Fatalf("expected nil items from stub, got %#v", items)
	}
}

func TestPluginHandlerBackupStubReturnsError(t *testing.T) {
	t.Parallel()

	h := &PluginHandler{}
	result, err := h.Backup(context.Background(), BackupItem{Name: "ignored"}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected platform-not-supported error from PluginHandler.Backup on non-Linux")
	}
	if result != nil {
		t.Fatalf("expected nil result from stub, got %#v", result)
	}
}

func TestPluginHandlerRestoreStubReturnsError(t *testing.T) {
	t.Parallel()

	h := &PluginHandler{}
	err := h.Restore(context.Background(), BackupItem{Name: "ignored"}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected platform-not-supported error from PluginHandler.Restore on non-Linux")
	}
}

func TestPluginHandlerBackupChunkedStubReturnsError(t *testing.T) {
	t.Parallel()

	h := &PluginHandler{}
	id, err := h.BackupChunked(context.Background(), BackupItem{Name: "ignored"}, nil, nil)
	if err == nil {
		t.Fatal("expected platform-not-supported error from PluginHandler.BackupChunked on non-Linux")
	}
	if id != (dedup.ID{}) {
		t.Fatalf("expected zero dedup.ID from stub, got %v", id)
	}
}

func TestPluginHandlerRestoreChunkedStubReturnsError(t *testing.T) {
	t.Parallel()

	h := &PluginHandler{}
	err := h.RestoreChunked(context.Background(), BackupItem{Name: "ignored"}, nil, dedup.ID{}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected platform-not-supported error from PluginHandler.RestoreChunked on non-Linux")
	}
}

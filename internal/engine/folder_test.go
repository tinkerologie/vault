package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFolderHandlerListItems(t *testing.T) {
	t.Parallel()
	h, err := NewFolderHandler()
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	items, err := h.ListItems()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// On macOS test hosts /boot does not exist, so items may be empty.
	// Just verify the slice is non-nil and any present item has the right shape.
	if items == nil {
		t.Error("expected non-nil items slice")
	}
	for _, it := range items {
		if it.Type != "folder" {
			t.Errorf("expected type=folder, got %q", it.Type)
		}
	}
}

func TestFolderHandlerBackupRestoreRoundTrip(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	subdir := filepath.Join(src, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "b.txt"), []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := &FolderHandler{}
	dest := t.TempDir()
	progress := func(string, int, string) {}
	item := BackupItem{
		Name:     "test-folder",
		Type:     "folder",
		Settings: map[string]any{"path": src},
	}

	res, err := h.Backup(context.Background(), item, dest, progress)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if !res.Success {
		t.Error("expected success")
	}
	if _, err := os.Stat(filepath.Join(dest, "data.tar.gz")); err != nil {
		t.Errorf("archive not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "folder_meta.json")); err != nil {
		t.Errorf("metadata not written: %v", err)
	}

	// Restore to a new location using restore_destination override.
	restoreDest := filepath.Join(t.TempDir(), "restored")
	restoreItem := BackupItem{
		Name: "test-folder",
		Type: "folder",
		Settings: map[string]any{
			"restore_destination": restoreDest,
		},
	}
	if err := h.Restore(context.Background(), restoreItem, dest, progress); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(restoreDest, "a.txt")); err != nil {
		t.Errorf("restore missing a.txt: %v", err)
	} else if string(data) != "hello" {
		t.Errorf("a.txt content mismatch: %q", data)
	}
}

func TestFolderHandlerBackupNoPath(t *testing.T) {
	t.Parallel()
	h := &FolderHandler{}
	_, err := h.Backup(context.Background(), BackupItem{Name: "x", Settings: map[string]any{}}, t.TempDir(), func(string, int, string) {})
	if err == nil {
		t.Error("expected error for missing path")
	}
}

func TestFolderHandlerBackupBadPath(t *testing.T) {
	t.Parallel()
	h := &FolderHandler{}
	item := BackupItem{Name: "x", Settings: map[string]any{"path": "/nonexistent-folder-xyz-abc"}}
	_, err := h.Backup(context.Background(), item, t.TempDir(), func(string, int, string) {})
	if err == nil {
		t.Error("expected error for missing src path")
	}
}

func TestFolderHandlerRestoreNoPath(t *testing.T) {
	t.Parallel()
	h := &FolderHandler{}
	item := BackupItem{Name: "x", Settings: map[string]any{}}
	err := h.Restore(context.Background(), item, t.TempDir(), func(string, int, string) {})
	if err == nil {
		t.Error("expected error for missing destination")
	}
}

func TestFolderHandlerRestoreUsesMeta(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &FolderHandler{}
	stage := t.TempDir()
	if _, err := h.Backup(context.Background(),
		BackupItem{Name: "x", Settings: map[string]any{"path": src}},
		stage, func(string, int, string) {}); err != nil {
		t.Fatal(err)
	}

	// Restore with no settings — should fall back to metadata path.
	if err := h.Restore(context.Background(),
		BackupItem{Name: "x", Settings: map[string]any{}}, stage,
		func(string, int, string) {}); err != nil {
		t.Errorf("restore via metadata: %v", err)
	}
}

func TestFolderHandlerRestoreMissingArchive(t *testing.T) {
	t.Parallel()
	h := &FolderHandler{}
	stage := t.TempDir()
	dest := filepath.Join(t.TempDir(), "dest")
	item := BackupItem{Name: "x", Settings: map[string]any{"restore_destination": dest}}
	err := h.Restore(context.Background(), item, stage, func(string, int, string) {})
	if err == nil {
		t.Error("expected error for missing archive")
	}
}

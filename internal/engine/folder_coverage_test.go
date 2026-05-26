package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFolderBackup_IncrementalChangedSince drives the changed_since
// branch of FolderHandler.Backup which delegates to tarDirectoryFiltered
// instead of tarDirectory.
func TestFolderBackup_IncrementalChangedSince(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "old.txt"), []byte("old"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Set old mtime so the file is "before" changed_since.
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(src, "old.txt"), past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Now create a "new" file that should be included.
	if err := os.WriteFile(filepath.Join(src, "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatalf("setup new: %v", err)
	}

	h := &FolderHandler{}
	dest := t.TempDir()
	item := BackupItem{
		Name: "incremental-folder",
		Type: "folder",
		Settings: map[string]any{
			"path":          src,
			"changed_since": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
		},
		Compression: CompressionNone,
	}
	res, err := h.Backup(context.Background(), item, dest, func(string, int, string) {})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if !res.Success {
		t.Error("expected success")
	}
	// Archive should exist.
	if _, err := os.Stat(filepath.Join(dest, "data.tar")); err != nil {
		t.Errorf("expected data.tar archive: %v", err)
	}
}

// TestFolderBackup_InvalidChangedSinceFallsBackToFull drives the
// branch where `changed_since` is malformed RFC3339 — the parse
// errors silently and Backup proceeds with a full tarDirectory.
func TestFolderBackup_InvalidChangedSinceFallsBackToFull(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	h := &FolderHandler{}
	dest := t.TempDir()
	item := BackupItem{
		Name: "garbage-changed-since",
		Type: "folder",
		Settings: map[string]any{
			"path":          src,
			"changed_since": "not-rfc3339",
		},
		Compression: CompressionNone,
	}
	res, err := h.Backup(context.Background(), item, dest, func(string, int, string) {})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if !res.Success {
		t.Error("expected success despite garbage changed_since")
	}
}

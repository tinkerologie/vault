//go:build !windows

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
)

func newChunkTestRepo(t *testing.T) *dedup.Repo {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repoDir := t.TempDir()
	adapter, err := storage.NewAdapter("local", `{"path":"`+repoDir+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.CloseAdapter(adapter) })
	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "walk-branches", Type: "local", Config: `{"path":"` + repoDir + `"}`, DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := dedup.InitRepo(database, adapter, destID, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// TestFolderBackupChunked_WalkErrBranches pins both halves of the walkErr
// guard added for #174: an unlistable SUBDIRECTORY is skipped-and-logged,
// while an unlistable SOURCE ROOT fails the item — silently succeeding with
// an empty manifest would mask a broken backup.
func TestFolderBackupChunked_WalkErrBranches(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits are ineffective as root")
	}
	h := &FolderHandler{}

	t.Run("unlistable-subdir-is-skipped", func(t *testing.T) {
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "ok.txt"), []byte("readable"), 0o644); err != nil {
			t.Fatal(err)
		}
		locked := filepath.Join(src, "locked")
		if err := os.Mkdir(locked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

		repo := newChunkTestRepo(t)
		item := BackupItem{Name: "t", Type: "folder", Settings: map[string]any{"path": src}}
		manifestID, err := h.BackupChunked(context.Background(), item, repo, nil)
		if err != nil {
			t.Fatalf("unlistable subdir must be skipped, got: %v", err)
		}
		if err := repo.Flush(); err != nil {
			t.Fatal(err)
		}
		mf, err := repo.GetManifest(manifestID)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := mf.Files["ok.txt"]; !ok {
			t.Error("readable file missing from manifest")
		}
	})

	t.Run("unlistable-source-root-fails", func(t *testing.T) {
		src := t.TempDir()
		if err := os.Chmod(src, 0o311); err != nil { // traversable, not listable
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(src, 0o755) })

		repo := newChunkTestRepo(t)
		item := BackupItem{Name: "t", Type: "folder", Settings: map[string]any{"path": src}}
		if _, err := h.BackupChunked(context.Background(), item, repo, nil); err == nil {
			t.Fatal("an unlistable source root must fail the item, not succeed empty")
		}
	})
}

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

// TestFolderBackupChunked_SkipsInaccessible pins the #174 contract: an
// unreadable file inside the tree is skipped with a log line — matching the
// classic tar path — instead of failing the whole item.
func TestFolderBackupChunked_SkipsInaccessible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits are ineffective as root")
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "ok.txt"), []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "secret.txt"), []byte("nope"), 0o000); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repoDir := t.TempDir()
	adapter, err := storage.NewAdapter("local", `{"path":"`+repoDir+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.CloseAdapter(adapter)
	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "t174", Type: "local", Config: `{"path":"` + repoDir + `"}`, DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	repo, err := dedup.InitRepo(database, adapter, destID, key)
	if err != nil {
		t.Fatal(err)
	}

	h := &FolderHandler{}
	item := BackupItem{Name: "t", Type: "folder", Settings: map[string]any{"path": src}}
	manifestID, err := h.BackupChunked(context.Background(), item, repo, nil)
	if err != nil {
		t.Fatalf("BackupChunked must skip unreadable files, got: %v", err)
	}
	if err := repo.Flush(); err != nil { // the runner flushes once per run
		t.Fatal(err)
	}
	mf, err := repo.GetManifest(manifestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mf.Files["ok.txt"]; !ok {
		t.Error("readable file missing from manifest")
	}
	if _, ok := mf.Files["secret.txt"]; ok {
		t.Error("unreadable file should have been skipped, not recorded with chunks")
	}
}

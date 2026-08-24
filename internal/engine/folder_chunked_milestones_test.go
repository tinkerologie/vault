//go:build !windows

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// TestFolderBackupChunked_PhaseMilestones: the dedup folder walk narrates
// its phases with pct >= 0 milestones (walk start, chunking complete) in
// addition to the terminal manifest-written line, so a flash-drive backup on
// a dedup destination stops being a single-line log. The per-file -1
// heartbeats stay heartbeats — the runner drops them from the run log
// (#328 QA).
func TestFolderBackupChunked_PhaseMilestones(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "vault.cfg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "notes.txt"), []byte("hello"), 0o644); err != nil {
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
		Name: "milestones", Type: "local", Config: `{"path":"` + repoDir + `"}`, DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	repo, err := dedup.InitRepo(database, adapter, destID, key)
	if err != nil {
		t.Fatal(err)
	}

	var msgs []string
	progress := func(_ string, _ int, msg string) { msgs = append(msgs, msg) }

	h := &FolderHandler{}
	item := BackupItem{Name: "Flash Drive", Type: "folder", Settings: map[string]any{"path": src}}
	if _, err := h.BackupChunked(context.Background(), item, repo, nil, progress); err != nil {
		t.Fatalf("BackupChunked: %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(msgs, "\n")
	cases := []struct {
		name string
		want string
	}{
		{name: "walk phase milestone narrated", want: "walking source tree"},
		{name: "chunking phase milestone narrated", want: "chunking complete"},
		{name: "manifest milestone narrated", want: "manifest written"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(joined, tc.want) {
				t.Errorf("phase milestone missing %q; got:\n%s", tc.want, joined)
			}
		})
	}
}

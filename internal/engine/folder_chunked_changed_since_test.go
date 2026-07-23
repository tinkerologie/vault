package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// TestFolderBackupChunked_ChangedSince verifies that BackupChunked
// honours the changed_since setting for differential backups.
//
// Boundary convention: a file whose mtime equals changed_since is
// treated as "not changed" (skipped). This matches pathChangedSince
// and tarDirectoryFiltered in the classic tar path.
func TestFolderBackupChunked_ChangedSince(t *testing.T) {
	t.Parallel()

	// Use a fixed reference timestamp well in the past so we can
	// place files at "older", "equal", and "newer" offsets.
	changedSince := time.Now().Add(-2 * time.Hour).Truncate(time.Second)

	tests := []struct {
		name           string
		settings       map[string]any
		files          []testFile
		wantInManifest []string
		wantExcluded   []string
	}{
		{
			name: "skips old files, includes new files and directory entries",
			settings: map[string]any{
				"changed_since": changedSince.Format(time.RFC3339),
			},
			files: []testFile{
				{name: "old.txt", content: "old", offset: -3 * time.Hour},
				{name: "new.txt", content: "new", offset: +2 * time.Hour},
				{name: "subdir/sub_old.txt", content: "sub old", offset: -3 * time.Hour},
			},
			wantInManifest: []string{"new.txt", "subdir"},
			wantExcluded:   []string{"old.txt", filepath.Join("subdir", "sub_old.txt")},
		},
		{
			name:     "omitting changed_since produces a full backup",
			settings: map[string]any{},
			files: []testFile{
				{name: "a.txt", content: "aaa", offset: -3 * time.Hour},
			},
			wantInManifest: []string{"a.txt"},
		},
		{
			name: "malformed changed_since falls back to full backup",
			settings: map[string]any{
				"changed_since": "not-rfc3339",
			},
			files: []testFile{
				{name: "b.txt", content: "bbb", offset: -3 * time.Hour},
			},
			wantInManifest: []string{"b.txt"},
		},
		{
			name: "file with mtime equal to changed_since is excluded",
			settings: map[string]any{
				"changed_since": changedSince.Format(time.RFC3339),
			},
			files: []testFile{
				{name: "boundary.txt", content: "equal", atExact: changedSince},
				{name: "after.txt", content: "after", offset: +1 * time.Hour},
			},
			wantInManifest: []string{"after.txt"},
			wantExcluded:   []string{"boundary.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := t.TempDir()
			for _, tf := range tt.files {
				tf.write(t, src, changedSince)
			}

			repo, _, cleanup := dedup.NewTestRepoForEngine(t)
			defer cleanup()

			tt.settings["path"] = src

			h := &FolderHandler{}
			manifestID, err := h.BackupChunked(context.Background(), BackupItem{
				Name:     "test-folder",
				Type:     "folder",
				Settings: tt.settings,
			}, repo, func(string, int, string) {})
			if err != nil {
				t.Fatalf("BackupChunked: %v", err)
			}
			if err := repo.Flush(); err != nil {
				t.Fatalf("Flush: %v", err)
			}

			m, err := repo.GetManifest(manifestID)
			if err != nil {
				t.Fatalf("GetManifest: %v", err)
			}

			for _, name := range tt.wantInManifest {
				if _, ok := m.Files[name]; !ok {
					t.Errorf("expected %q in manifest", name)
				}
			}
			for _, name := range tt.wantExcluded {
				if _, ok := m.Files[name]; ok {
					t.Errorf("expected %q to be excluded from manifest", name)
				}
			}
		})
	}
}

// testFile describes a file to create on disk for a test case.
type testFile struct {
	name    string
	content string
	// offset is relative to changedSince; negative = before, positive = after.
	// Ignored when atExact is non-zero.
	offset time.Duration
	// atExact pins the file mtime to this timestamp.
	atExact time.Time
}

func (tf testFile) write(t *testing.T, dir string, changedSince time.Time) {
	t.Helper()

	full := filepath.Join(dir, tf.name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", tf.name, err)
	}
	if err := os.WriteFile(full, []byte(tf.content), 0o600); err != nil {
		t.Fatalf("write %s: %v", tf.name, err)
	}

	var mtime time.Time
	if !tf.atExact.IsZero() {
		mtime = tf.atExact
	} else {
		mtime = changedSince.Add(tf.offset)
	}
	if err := os.Chtimes(full, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", tf.name, err)
	}
}

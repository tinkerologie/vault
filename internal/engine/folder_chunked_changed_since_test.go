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
//
// changed_since filtering only applies when a parent manifest is supplied.
// Without a parent (a full backup, a newly-added item, or a missing parent
// manifest), changed_since is ignored and the tree is chunked FULLY, so a
// brand-new item with old file mtimes is never silently recorded empty
// (issue #320). With a parent manifest, unchanged files are carried forward
// from the parent so the resulting manifest stays complete for single-point
// restore.
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
			name: "no parent: changed_since ignored, all files included (new item safety)",
			settings: map[string]any{
				"changed_since": changedSince.Format(time.RFC3339),
			},
			files: []testFile{
				{name: "old.txt", content: "old", offset: -3 * time.Hour},
				{name: "new.txt", content: "new", offset: +2 * time.Hour},
				{name: "subdir/sub_old.txt", content: "sub old", offset: -3 * time.Hour},
			},
			wantInManifest: []string{"old.txt", "new.txt", "subdir", filepath.Join("subdir", "sub_old.txt")},
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
			name: "no parent: boundary mtime file included (full walk)",
			settings: map[string]any{
				"changed_since": changedSince.Format(time.RFC3339),
			},
			files: []testFile{
				{name: "boundary.txt", content: "equal", atExact: changedSince},
				{name: "after.txt", content: "after", offset: +1 * time.Hour},
			},
			wantInManifest: []string{"boundary.txt", "after.txt"},
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
			}, repo, nil, func(string, int, string) {})
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

// TestFolderBackupChunked_ChangedSince_CarriesForward is the regression test
// for the dedup-path half of issue #320. When a parent manifest is supplied,
// a file skipped by changed_since must be carried forward from the parent so
// the differential manifest stays complete and single-point restore does not
// drop base files.
func TestFolderBackupChunked_ChangedSince_CarriesForward(t *testing.T) {
	t.Parallel()

	changedSince := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	src := t.TempDir()

	old := testFile{name: "old.txt", content: "old", offset: -3 * time.Hour}
	boundary := testFile{name: "boundary.txt", content: "equal", atExact: changedSince}
	newFile := testFile{name: "new.txt", content: "new", offset: +2 * time.Hour}
	old.write(t, src, changedSince)
	boundary.write(t, src, changedSince)
	newFile.write(t, src, changedSince)

	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &FolderHandler{}
	item := BackupItem{Name: "test-folder", Type: "folder", Settings: map[string]any{"path": src}}

	// Full backup: both files present (no changed_since).
	fullID, err := h.BackupChunked(context.Background(), item, repo, nil, func(string, int, string) {})
	if err != nil {
		t.Fatalf("full BackupChunked: %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	parent, err := repo.GetManifest(fullID)
	if err != nil {
		t.Fatalf("GetManifest(parent): %v", err)
	}

	// Differential: old.txt is unchanged (mtime predates changed_since), so it
	// must be carried forward from the parent; new.txt is newly chunked.
	item.Settings["changed_since"] = changedSince.Format(time.RFC3339)
	diffID, err := h.BackupChunked(context.Background(), item, repo, &parent, func(string, int, string) {})
	if err != nil {
		t.Fatalf("differential BackupChunked: %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	m, err := repo.GetManifest(diffID)
	if err != nil {
		t.Fatalf("GetManifest(diff): %v", err)
	}

	for _, name := range []string{"old.txt", "boundary.txt", "new.txt"} {
		if _, ok := m.Files[name]; !ok {
			t.Errorf("differential manifest missing %q — unchanged files must be carried forward", name)
		}
	}
}

// TestFolderBackupChunked_ChangedSince_NewFileNotInParent is the regression
// test for the folder half of issue #320's data-loss class: a file added
// between backups whose mtime predates changed_since (e.g. copied in with
// timestamps preserved) is NOT in the parent manifest, so carry-forward must
// fall through to a full chunk rather than silently dropping the entry.
func TestFolderBackupChunked_ChangedSince_NewFileNotInParent(t *testing.T) {
	t.Parallel()

	changedSince := time.Now().Add(-2 * time.Hour).Truncate(time.Second)

	tests := []struct {
		name      string
		addedFile testFile
	}{
		{
			name:      "new file with preserved old mtime is chunked, not dropped",
			addedFile: testFile{name: "added.txt", content: "added", offset: -3 * time.Hour},
		},
		{
			name:      "new file with fresh mtime is chunked",
			addedFile: testFile{name: "added.txt", content: "added", offset: +1 * time.Hour},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := t.TempDir()
			old := testFile{name: "old.txt", content: "old", offset: -3 * time.Hour}
			old.write(t, src, changedSince)

			repo, _, cleanup := dedup.NewTestRepoForEngine(t)
			defer cleanup()

			h := &FolderHandler{}
			item := BackupItem{Name: "test-folder", Type: "folder", Settings: map[string]any{"path": src}}

			// Full backup: only old.txt present.
			fullID, err := h.BackupChunked(context.Background(), item, repo, nil, func(string, int, string) {})
			if err != nil {
				t.Fatalf("full BackupChunked: %v", err)
			}
			if err := repo.Flush(); err != nil {
				t.Fatalf("Flush: %v", err)
			}
			parent, err := repo.GetManifest(fullID)
			if err != nil {
				t.Fatalf("GetManifest(parent): %v", err)
			}

			// Add a new file after the full backup.
			tt.addedFile.write(t, src, changedSince)

			// Differential: old.txt carried forward; added.txt must be present.
			item.Settings["changed_since"] = changedSince.Format(time.RFC3339)
			diffID, err := h.BackupChunked(context.Background(), item, repo, &parent, func(string, int, string) {})
			if err != nil {
				t.Fatalf("differential BackupChunked: %v", err)
			}
			if err := repo.Flush(); err != nil {
				t.Fatalf("Flush: %v", err)
			}
			m, err := repo.GetManifest(diffID)
			if err != nil {
				t.Fatalf("GetManifest(diff): %v", err)
			}

			if _, ok := m.Files["old.txt"]; !ok {
				t.Errorf("differential manifest missing %q — unchanged file not carried forward", "old.txt")
			}
			if _, ok := m.Files["added.txt"]; !ok {
				t.Errorf("differential manifest missing %q — new file silently dropped", "added.txt")
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

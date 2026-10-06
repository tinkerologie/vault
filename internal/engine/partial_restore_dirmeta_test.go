//go:build unix

package engine

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// A partial restore selects leaf files, so the directories containing them
// never match the include filter. They must still come back with their
// recorded mode and owner rather than the daemon's root:root 0755 (#442).

// backupDirMetaTree backs up src/3DS/{b.txt,Games/a.cia} plus an unselected
// src/Other/c.txt, with explicit modes so the umask cannot mask a regression.
func backupDirMetaTree(t *testing.T) (*FolderHandler, BackupItem, *dedup.Repo, dedup.ID, string) {
	t.Helper()
	src := t.TempDir()
	for _, d := range []string{"3DS/Games", "Other"} {
		if err := os.MkdirAll(filepath.Join(src, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"3DS/Games/a.cia", "3DS/b.txt", "Other/c.txt"} {
		if err := os.WriteFile(filepath.Join(src, f), []byte(f), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(src, f), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(src, "3DS"), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "3DS/Games"), 0o770); err != nil {
		t.Fatal(err)
	}

	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	t.Cleanup(cleanup)
	h := &FolderHandler{}
	item := BackupItem{Name: "C-Data", Type: "folder", Settings: map[string]any{"path": src}}
	manifestID, err := h.BackupChunked(context.Background(), item, repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	return h, item, repo, manifestID, src
}

func restoreSelection(t *testing.T, h *FolderHandler, item BackupItem, repo *dedup.Repo, id dedup.ID, dst string, paths []string, extra map[string]any) {
	t.Helper()
	settings := map[string]any{"path": item.Settings["path"], "restore_file_paths": paths}
	for k, v := range extra {
		settings[k] = v
	}
	item.Settings = settings
	if err := h.RestoreChunked(context.Background(), item, repo, id, dst, nil); err != nil {
		t.Fatalf("RestoreChunked(%v): %v", paths, err)
	}
}

func TestFolderChunkedPartialRestoreAppliesAncestorDirModes(t *testing.T) {
	h, item, repo, id, src := backupDirMetaTree(t)

	tests := []struct {
		name     string
		paths    []string
		present  []string
		absent   []string
		wantDirs map[string]os.FileMode
	}{
		{
			name:     "descendant files only (UI picker)",
			paths:    []string{"3DS/Games/a.cia", "3DS/b.txt"},
			present:  []string{"3DS/Games/a.cia", "3DS/b.txt"},
			absent:   []string{"Other"},
			wantDirs: map[string]os.FileMode{"3DS": 0o775, "3DS/Games": 0o770},
		},
		{
			name:     "single nested file",
			paths:    []string{"3DS/Games/a.cia"},
			present:  []string{"3DS/Games/a.cia"},
			absent:   []string{"3DS/b.txt", "Other"},
			wantDirs: map[string]os.FileMode{"3DS": 0o775, "3DS/Games": 0o770},
		},
		{
			name:     "exact directory",
			paths:    []string{"3DS"},
			present:  []string{"3DS/Games/a.cia", "3DS/b.txt"},
			absent:   []string{"Other"},
			wantDirs: map[string]os.FileMode{"3DS": 0o775, "3DS/Games": 0o770},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dst := t.TempDir()
			if err := os.Chmod(dst, 0o711); err != nil {
				t.Fatal(err)
			}
			restoreSelection(t, h, item, repo, id, dst, tc.paths, nil)

			for d, want := range tc.wantDirs {
				if got := modeOf(t, filepath.Join(dst, d)); got != want {
					t.Errorf("%s mode = %04o, want %04o", d, got, want)
				}
				srcUID, srcGID := fileOwner(lstat(t, filepath.Join(src, d)))
				gotUID, gotGID := fileOwner(lstat(t, filepath.Join(dst, d)))
				if srcUID >= 0 && (gotUID != srcUID || gotGID != srcGID) {
					t.Errorf("%s owner = %d:%d, want %d:%d", d, gotUID, gotGID, srcUID, srcGID)
				}
			}
			for _, f := range tc.present {
				if got := modeOf(t, filepath.Join(dst, f)); got != 0o640 {
					t.Errorf("%s mode = %04o, want 0640", f, got)
				}
			}
			for _, p := range tc.absent {
				if _, err := os.Lstat(filepath.Join(dst, p)); !os.IsNotExist(err) {
					t.Errorf("%s exists after partial restore (err=%v), want absent", p, err)
				}
			}
			if got := modeOf(t, dst); got != 0o711 {
				t.Errorf("destination root mode = %04o, want unchanged 0711", got)
			}
		})
	}
}

func TestFolderChunkedPartialRestoreCorrectsStaleAncestor(t *testing.T) {
	h, item, repo, id, _ := backupDirMetaTree(t)
	dst := t.TempDir()
	if err := os.Mkdir(filepath.Join(dst, "3DS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dst, "3DS"), 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dst, "3DS", "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	restoreSelection(t, h, item, repo, id, dst, []string{"3DS/b.txt"},
		map[string]any{SettingCleanDestination: true})

	if got := modeOf(t, filepath.Join(dst, "3DS")); got != 0o775 {
		t.Errorf("3DS mode = %04o, want 0775", got)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("unselected existing file was removed by partial restore: %v", err)
	}
}

// Root-only: proves the recorded owner (not the daemon's) lands on the
// ancestor, the exact nobody:users vs root:root symptom from the report.
func TestFolderChunkedPartialRestoreAppliesAncestorOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to chown the source tree")
	}
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "3DS"), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "3DS/b.txt"), []byte("b"), 0o664); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"3DS", "3DS/b.txt"} {
		if err := os.Lchown(filepath.Join(src, p), 65534, 100); err != nil {
			t.Fatal(err)
		}
	}

	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	h := &FolderHandler{}
	item := BackupItem{Name: "C-Data", Type: "folder", Settings: map[string]any{"path": src}}
	id, err := h.BackupChunked(context.Background(), item, repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	restoreSelection(t, h, item, repo, id, dst, []string{"3DS/b.txt"}, nil)
	if uid, gid := fileOwner(lstat(t, filepath.Join(dst, "3DS"))); uid != 65534 || gid != 100 {
		t.Errorf("3DS owner = %d:%d, want 65534:100", uid, gid)
	}
}

// The classic (non-deduplicated) tar path has the same filter, so it gets the
// same guarantee.
func TestUntarDirectoryFilteredAppliesAncestorDirModes(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "data.tar")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	entries := []struct {
		name string
		mode int64
		dir  bool
	}{
		{"3DS/", 0o775, true},
		{"3DS/Games/", 0o770, true},
		{"3DS/Games/a.cia", 0o640, false},
		{"3DS/b.txt", 0o640, false},
		{"Other/", 0o700, true},
		{"Other/c.txt", 0o640, false},
	}
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: tar.TypeReg, Size: int64(len(e.name))}
		if e.dir {
			hdr.Typeflag, hdr.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !e.dir {
			if _, err := tw.Write([]byte(e.name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := untarDirectoryFiltered(context.Background(), archive, dst, []string{"3DS/Games/a.cia"}); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, filepath.Join(dst, "3DS")); got != 0o775 {
		t.Errorf("3DS mode = %04o, want 0775", got)
	}
	if got := modeOf(t, filepath.Join(dst, "3DS/Games")); got != 0o770 {
		t.Errorf("3DS/Games mode = %04o, want 0770", got)
	}
	for _, p := range []string{"3DS/b.txt", "Other"} {
		if _, err := os.Lstat(filepath.Join(dst, p)); !os.IsNotExist(err) {
			t.Errorf("%s exists after partial restore (err=%v), want absent", p, err)
		}
	}
}

func lstat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

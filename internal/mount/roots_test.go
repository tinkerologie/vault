package mount

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// redirectRoots points the package mount roots at a temp tree so the tests
// never touch the host /mnt or /tmp (#446). Not parallel-safe.
func redirectRoots(t *testing.T) (addons, legacy, tmp string) {
	t.Helper()
	root := t.TempDir()
	addons = filepath.Join(root, "mnt", "addons")
	legacy = filepath.Join(root, "mnt", "vault-fuse")
	tmp = filepath.Join(root, "tmp")
	oldAddons, oldLegacy, oldTemp := addonsRoot, legacyMountRoot, tempRoot
	addonsRoot, legacyMountRoot = addons, legacy
	tempRoot = func() string { return tmp }
	t.Cleanup(func() { addonsRoot, legacyMountRoot, tempRoot = oldAddons, oldLegacy, oldTemp })
	return addons, legacy, tmp
}

func assertAbsent(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("%s exists (err=%v), want absent", p, err)
	}
}

func TestNewManagerResolvesDefaultWithoutCreating(t *testing.T) {
	addons, legacy, tmp := redirectRoots(t)

	m := NewManager(nil, nil, nil)
	if want := filepath.Join(tmp, mountRootName); m.baseMountDir != want {
		t.Errorf("base without /mnt/addons = %s, want %s", m.baseMountDir, want)
	}
	assertAbsent(t, filepath.Join(tmp, mountRootName))
	assertAbsent(t, legacy)

	if err := os.MkdirAll(addons, 0o755); err != nil {
		t.Fatal(err)
	}
	m = NewManager(nil, nil, nil)
	if want := filepath.Join(addons, mountRootName); m.baseMountDir != want || !m.baseIsDefault {
		t.Errorf("base with /mnt/addons = %s (default=%v), want %s", m.baseMountDir, m.baseIsDefault, want)
	}
	assertAbsent(t, filepath.Join(addons, mountRootName))
	assertAbsent(t, legacy)
}

func TestNewManagerConfiguredBaseWins(t *testing.T) {
	addons, _, tmp := redirectRoots(t)
	if err := os.MkdirAll(addons, 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	custom := filepath.Join(t.TempDir(), "custom")
	if err := d.SetSetting("fuse_mount_base_dir", custom); err != nil {
		t.Fatal(err)
	}

	m := NewManager(d, nil, nil)
	if m.baseMountDir != custom || m.baseIsDefault {
		t.Errorf("base = %s (default=%v), want configured %s", m.baseMountDir, m.baseIsDefault, custom)
	}
	assertAbsent(t, filepath.Join(addons, mountRootName))
	assertAbsent(t, filepath.Join(tmp, mountRootName))
	assertAbsent(t, custom)
}

func TestSessionMountDir(t *testing.T) {
	addons, _, tmp := redirectRoots(t)
	if err := os.MkdirAll(addons, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("default base", func(t *testing.T) {
		m := NewManager(nil, nil, nil)
		dir, err := m.sessionMountDir("", 7)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(addons, mountRootName, "mount-7"); dir != want {
			t.Errorf("dir = %s, want %s", dir, want)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("session dir not created: %v", err)
		}
	})

	t.Run("explicit target leaves base alone", func(t *testing.T) {
		m := NewManager(nil, nil, nil)
		m.baseMountDir = filepath.Join(t.TempDir(), "unused-base")
		target := filepath.Join(t.TempDir(), "explicit")
		dir, err := m.sessionMountDir(target, 8)
		if err != nil || dir != target {
			t.Fatalf("dir = %s, err = %v, want %s", dir, err, target)
		}
		assertAbsent(t, m.baseMountDir)
	})

	t.Run("default falls back to temp dir", func(t *testing.T) {
		m := NewManager(nil, nil, nil)
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		m.baseMountDir = blocker // MkdirAll under a regular file fails
		dir, err := m.sessionMountDir("", 9)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(tmp, mountRootName, "mount-9"); dir != want {
			t.Errorf("dir = %s, want fallback %s", dir, want)
		}
	})

	t.Run("configured base error is returned", func(t *testing.T) {
		m := NewManager(nil, nil, nil)
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		m.SetBaseMountDir(blocker)
		if dir, err := m.sessionMountDir("", 10); err == nil {
			t.Errorf("dir = %s, want error for unusable configured base", dir)
		}
		assertAbsent(t, filepath.Join(tmp, mountRootName, "mount-10"))
	})
}

func TestCleanupLegacyRoot(t *testing.T) {
	t.Run("empty sessions and root removed", func(t *testing.T) {
		_, legacy, _ := redirectRoots(t)
		for _, d := range []string{"mount-1", "mount-22"} {
			if err := os.MkdirAll(filepath.Join(legacy, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		NewManager(nil, nil, nil).cleanupLegacyRoot()
		assertAbsent(t, legacy)
	})

	t.Run("non-empty, foreign and symlinked entries kept", func(t *testing.T) {
		_, legacy, _ := redirectRoots(t)
		data := filepath.Join(legacy, "mount-3", "file.txt")
		if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(data, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		foreign := filepath.Join(legacy, "not-a-session")
		if err := os.MkdirAll(foreign, 0o755); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(legacy, "mount-4")); err != nil {
			t.Fatal(err)
		}

		NewManager(nil, nil, nil).cleanupLegacyRoot()
		for _, p := range []string{data, foreign, filepath.Join(legacy, "mount-4"), outside} {
			if _, err := os.Lstat(p); err != nil {
				t.Errorf("%s removed, want kept: %v", p, err)
			}
		}
	})

	t.Run("symlinked root kept", func(t *testing.T) {
		_, legacy, _ := redirectRoots(t)
		target := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, legacy); err != nil {
			t.Fatal(err)
		}
		NewManager(nil, nil, nil).cleanupLegacyRoot()
		if _, err := os.Lstat(legacy); err != nil {
			t.Errorf("symlinked legacy root removed: %v", err)
		}
	})

	t.Run("configured legacy base kept", func(t *testing.T) {
		_, legacy, _ := redirectRoots(t)
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		m := NewManager(nil, nil, nil)
		m.SetBaseMountDir(legacy)
		m.cleanupLegacyRoot()
		if _, err := os.Lstat(legacy); err != nil {
			t.Errorf("legacy root in use as configured base was removed: %v", err)
		}
	})
}

// stubUnmount replaces the platform unmount for the test. Not parallel-safe.
func stubUnmount(t *testing.T, fn func(string) error) {
	t.Helper()
	old := unmountPath
	unmountPath = fn
	t.Cleanup(func() { unmountPath = old })
}

func TestCleanupStaleRemovesOnlyContainedSessionDirs(t *testing.T) {
	_, legacy, _ := redirectRoots(t)
	stubUnmount(t, func(string) error { return nil })
	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"/tmp/x"}`})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "j", StorageDestID: destID, BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}

	inLegacy := filepath.Join(legacy, "mount-1")
	outside := filepath.Join(t.TempDir(), "explicit")
	for _, p := range []string{inLegacy, outside} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		id, err := d.CreateMountSession(db.MountSession{JobID: jobID, StorageDestID: destID, MountPath: p})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.UpdateMountSessionStatus(id, "active", ""); err != nil {
			t.Fatal(err)
		}
	}

	m := NewManager(d, nil, nil)
	m.CleanupStale(t.Context())
	assertAbsent(t, inLegacy)
	if _, err := os.Lstat(outside); err != nil {
		t.Errorf("explicit target outside mount roots removed: %v", err)
	}
}

func TestRemoveEmptyMountRoot(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	full := filepath.Join(root, "full")
	link := filepath.Join(root, "link")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "f"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(empty, link); err != nil {
		t.Fatal(err)
	}

	if err := RemoveEmptyMountRoot(filepath.Join(root, "missing")); err != nil {
		t.Errorf("missing root: %v", err)
	}
	if err := RemoveEmptyMountRoot(link); err != nil {
		t.Errorf("symlink root: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("symlink removed: %v", err)
	}
	if err := RemoveEmptyMountRoot(full); err == nil {
		t.Error("non-empty root: want error")
	}
	if err := RemoveEmptyMountRoot(empty); err != nil {
		t.Errorf("empty root: %v", err)
	}
	assertAbsent(t, empty)
}

func TestCleanupStaleKeepsDirWhenUnmountFails(t *testing.T) {
	_, legacy, _ := redirectRoots(t)
	var unmounted []string
	stubUnmount(t, func(p string) error {
		unmounted = append(unmounted, p)
		return errors.New("device busy")
	})
	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "d", Type: "local", Config: `{"path":"/tmp/x"}`})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "j", StorageDestID: destID, BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(legacy, "mount-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateMountSession(db.MountSession{JobID: jobID, StorageDestID: destID, MountPath: dir}); err != nil {
		t.Fatal(err)
	}

	NewManager(d, nil, nil).CleanupStale(t.Context())
	if len(unmounted) != 1 || unmounted[0] != dir {
		t.Errorf("unmounted = %v, want [%s]", unmounted, dir)
	}
	if _, err := os.Lstat(dir); err != nil {
		t.Errorf("session dir removed although unmount failed: %v", err)
	}
}

// A session whose unmount failed is marked stopped, so its leftover directory
// under the current base is only reclaimed by the next start's sweep.
func TestCleanupOrphanedSessionDirs(t *testing.T) {
	redirectRoots(t)
	m := NewManager(nil, nil, nil)
	base := t.TempDir()
	m.SetBaseMountDir(base)
	orphan := filepath.Join(base, "mount-5")
	busy := filepath.Join(base, "mount-6", "data")
	foreign := filepath.Join(base, "user-dir")
	for _, d := range []string{orphan, busy, foreign} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	m.cleanupOrphanedSessionDirs()
	assertAbsent(t, orphan)
	for _, p := range []string{busy, foreign, base} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s removed, want kept: %v", p, err)
		}
	}
}

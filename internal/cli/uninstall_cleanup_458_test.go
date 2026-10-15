package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/tempdir"
	"github.com/tinkerologie/vault/internal/unraid"
)

func skipIfRoot458(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
}

// readOnly458 makes dir non-writable for the rest of the test.
func readOnly458(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

func writeFile458(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// uninstallCfg458 returns a config whose every path lives under root.
func uninstallCfg458(root string) uninstallCleanupConfig {
	return uninstallCleanupConfig{
		DBPath:            filepath.Join(root, "config", "vault.db"),
		ConfigDir:         filepath.Join(root, "config"),
		BinaryPath:        filepath.Join(root, "bin", "vault"),
		RCScriptPath:      filepath.Join(root, "rc.vault"),
		PluginDir:         filepath.Join(root, "plugin"),
		LogPath:           filepath.Join(root, "vault.log"),
		PIDFile:           filepath.Join(root, "vault.pid"),
		HybridWorkingDir:  filepath.Join(root, "hybrid"),
		DefaultSnapshotDB: filepath.Join(root, "cache", ".vault", "vault.db"),
		CachePaths:        []string{filepath.Join(root, "cache"), "  "},
		FUSERoots:         []string{filepath.Join(root, "fuse")},
	}
}

func TestCleanupUninstallCmd458_RunE(t *testing.T) {
	root := t.TempDir()
	cfg := uninstallCfg458(root)
	mntDir := filepath.Join(root, "mnt")
	if err := os.MkdirAll(filepath.Join(mntDir, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unraid.SetMntBaseForTest(mntDir, filepath.Join(root, "mountinfo")))
	t.Cleanup(tempdir.SetCachePathsForTest([]string{filepath.Join(root, "cache")}))

	writeFile458(t, cfg.BinaryPath, "bin")
	writeFile458(t, filepath.Join(cfg.ConfigDir, "vault.cfg"), "X=1")
	writeFile458(t, filepath.Join(cfg.ConfigDir, "vault.key"), "key")

	flags := map[string]string{
		"db":                 cfg.DBPath,
		"config-dir":         cfg.ConfigDir,
		"binary-path":        cfg.BinaryPath,
		"rc-script":          cfg.RCScriptPath,
		"plugin-dir":         cfg.PluginDir,
		"log-path":           cfg.LogPath,
		"pid-file":           cfg.PIDFile,
		"hybrid-working-dir": cfg.HybridWorkingDir,
		"snapshot-path":      cfg.DefaultSnapshotDB,
	}
	fs := cleanupUninstallCmd.Flags()
	for name, val := range flags {
		f := fs.Lookup(name)
		def := f.DefValue
		if err := fs.Set(name, val); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = fs.Set(name, def)
			f.Changed = false
		})
	}

	if err := cleanupUninstallCmd.RunE(cleanupUninstallCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if _, err := os.Stat(cfg.BinaryPath); !os.IsNotExist(err) {
		t.Errorf("binary should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, "vault.cfg")); !os.IsNotExist(err) {
		t.Errorf("vault.cfg should be removed, stat err = %v", err)
	}
	assertServerKeyKept(t, cfg.ConfigDir)
}

func TestDefaultUninstallCleanupConfig458_UsesPreferredPool(t *testing.T) {
	root := t.TempDir()
	mntDir := filepath.Join(root, "mnt")
	if err := os.MkdirAll(filepath.Join(mntDir, "fastpool"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unraid.SetMntBaseForTest(mntDir, filepath.Join(root, "mountinfo")))
	cfg := defaultUninstallCleanupConfig()
	want := filepath.Join(mntDir, "fastpool", ".vault", "vault.db")
	if cfg.DefaultSnapshotDB != want {
		t.Errorf("DefaultSnapshotDB = %q, want %q", cfg.DefaultSnapshotDB, want)
	}
	if len(cfg.FUSERoots) == 0 {
		t.Error("FUSERoots should default to the mount roots")
	}
}

func TestRunUninstallCleanup458_ConfigDirIsBackupRoot(t *testing.T) {
	root := t.TempDir()
	cfg := uninstallCfg458(root)
	if err := os.MkdirAll(cfg.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateStorageDestination(db.StorageDestination{Name: "l", Type: "local", Config: `{"path":"` + cfg.ConfigDir + `"}`}); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	writeFile458(t, filepath.Join(cfg.ConfigDir, "backup.tar"), "data")
	// A non-empty FUSE root is kept (and logged), never force-removed.
	writeFile458(t, filepath.Join(cfg.FUSERoots[0], "user-file"), "x")

	if err := runUninstallCleanup(cfg); err != nil {
		t.Fatalf("runUninstallCleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, "backup.tar")); err != nil {
		t.Errorf("backup inside preserved config dir removed: %v", err)
	}
}

func TestRunUninstallCleanup458_ConfidentRemovesWholeConfigDir(t *testing.T) {
	root := t.TempDir()
	cfg := uninstallCfg458(root)
	if err := os.MkdirAll(cfg.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	backupRoot := filepath.Join(root, "backups")
	for _, dest := range []db.StorageDestination{
		{Name: "l", Type: "local", Config: `{"path":"` + backupRoot + `"}`},
		{Name: "bad", Type: "local", Config: `{not json`},
		{Name: "empty", Type: "local", Config: `{}`},
		{Name: "s3", Type: "s3", Config: `{}`},
	} {
		if _, err := d.CreateStorageDestination(dest); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.Close()
	writeFile458(t, filepath.Join(cfg.ConfigDir, "junk", "file"), "x")

	if err := runUninstallCleanup(cfg); err != nil {
		t.Fatalf("runUninstallCleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.ConfigDir, "junk")); !os.IsNotExist(err) {
		t.Errorf("config junk should be removed, stat err = %v", err)
	}
}

func TestDiscoverUninstallCleanupState458_Warnings(t *testing.T) {
	root := t.TempDir()
	cfg := uninstallCfg458(root)
	// DB path beneath a regular file → stat fails with ENOTDIR (warning).
	blocker := filepath.Join(root, "blocker")
	writeFile458(t, blocker, "x")
	cfg.DBPath = filepath.Join(blocker, "vault.db")
	// Hybrid DB is a directory → skipped.
	if err := os.MkdirAll(filepath.Join(cfg.HybridWorkingDir, "vault.db"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Snapshot DB is garbage → open warning.
	writeFile458(t, cfg.DefaultSnapshotDB, strings.Repeat("not sqlite ", 50))

	state, err := discoverUninstallCleanupState(cfg)
	if err == nil {
		t.Fatal("expected aggregated warnings")
	}
	if state.Confident {
		t.Error("state must not be confident")
	}
	if !strings.Contains(err.Error(), "stat ") {
		t.Errorf("warning missing stat entry: %v", err)
	}

	// runUninstallCleanup proceeds with the safe fallback, then fails to
	// remove the DB path that sits beneath a regular file.
	if err := runUninstallCleanup(cfg); err == nil || !strings.Contains(err.Error(), "remove ") {
		t.Fatalf("runUninstallCleanup err = %v, want remove error", err)
	}
}

func TestRunUninstallCleanup458_RemoveErrors(t *testing.T) {
	skipIfRoot458(t)

	cases := map[string]func(t *testing.T, cfg *uninstallCleanupConfig, root string){
		"stage": func(t *testing.T, cfg *uninstallCleanupConfig, root string) {
			cache := filepath.Join(root, "cache")
			writeFile458(t, filepath.Join(cache, tempdir.StageDirName, "f"), "x")
			readOnly458(t, cache)
		},
		"plugin": func(t *testing.T, cfg *uninstallCleanupConfig, root string) {
			ro := filepath.Join(root, "ro")
			writeFile458(t, filepath.Join(ro, "plugin", "f"), "x")
			cfg.PluginDir = filepath.Join(ro, "plugin")
			readOnly458(t, ro)
		},
		"hybrid": func(t *testing.T, cfg *uninstallCleanupConfig, root string) {
			ro := filepath.Join(root, "ro")
			writeFile458(t, filepath.Join(ro, "hybrid", "vault.db"), "")
			cfg.HybridWorkingDir = filepath.Join(ro, "hybrid")
			readOnly458(t, ro)
		},
		"snapshot": func(t *testing.T, cfg *uninstallCleanupConfig, root string) {
			ro := filepath.Join(root, "snap")
			writeFile458(t, filepath.Join(ro, "vault.db-wal"), "x")
			cfg.DefaultSnapshotDB = filepath.Join(ro, "vault.db")
			readOnly458(t, ro)
		},
		"config": func(t *testing.T, cfg *uninstallCleanupConfig, root string) {
			writeFile458(t, filepath.Join(cfg.ConfigDir, "vault.cfg"), "x")
			readOnly458(t, cfg.ConfigDir)
		},
		"configdir": func(t *testing.T, cfg *uninstallCleanupConfig, root string) {
			if err := os.MkdirAll(cfg.ConfigDir, 0o755); err != nil {
				t.Fatal(err)
			}
			d, err := db.Open(cfg.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			_ = d.Close()
			sub := filepath.Join(cfg.ConfigDir, "sub")
			writeFile458(t, filepath.Join(sub, "f"), "x")
			readOnly458(t, sub)
		},
		"tgz": func(t *testing.T, cfg *uninstallCleanupConfig, root string) {
			writeFile458(t, filepath.Join(cfg.ConfigDir, "pkg.tgz", "inner"), "x")
			readOnly458(t, filepath.Join(cfg.ConfigDir, "pkg.tgz"))
		},
		"txz": func(t *testing.T, cfg *uninstallCleanupConfig, root string) {
			writeFile458(t, filepath.Join(cfg.ConfigDir, "pkg.txz", "inner"), "x")
			readOnly458(t, filepath.Join(cfg.ConfigDir, "pkg.txz"))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			cfg := uninstallCfg458(root)
			setup(t, &cfg, root)
			if err := runUninstallCleanup(cfg); err == nil {
				t.Fatal("expected remove error")
			}
		})
	}
}

func TestCleanupConfigDir458_Branches(t *testing.T) {
	if err := cleanupConfigDir(uninstallCleanupConfig{ConfigDir: " "}, uninstallCleanupState{}); err != nil {
		t.Fatalf("empty config dir: %v", err)
	}

	skipIfRoot458(t)
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	nested := filepath.Join(configDir, "nested")
	keep := filepath.Join(nested, "keep")
	writeFile458(t, filepath.Join(keep, "data"), "x")
	writeFile458(t, filepath.Join(nested, "other", "f"), "x")
	readOnly458(t, nested)
	state := uninstallCleanupState{Confident: true, PreserveRoots: []string{keep}}
	if err := cleanupConfigDir(uninstallCleanupConfig{ConfigDir: configDir}, state); err == nil {
		t.Fatal("expected error removing child of read-only nested dir")
	}
}

func TestRemoveDirContentsExceptPreserved458_Errors(t *testing.T) {
	root := t.TempDir()
	if err := removeDirContentsExceptPreserved(filepath.Join(root, "absent"), nil); err != nil {
		t.Errorf("missing dir: %v", err)
	}

	skipIfRoot458(t)
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if err := removeDirContentsExceptPreserved(locked, nil); err == nil {
		t.Error("expected read error")
	}

	// Nested preserved descendant whose sibling cannot be removed.
	top := filepath.Join(root, "top")
	mid := filepath.Join(top, "mid")
	writeFile458(t, filepath.Join(mid, "keep", "x"), "x")
	writeFile458(t, filepath.Join(mid, "drop", "y"), "y")
	readOnly458(t, mid)
	if err := removeDirContentsExceptPreserved(top, []string{filepath.Join(mid, "keep")}); err == nil {
		t.Error("expected nested remove error")
	}
}

func TestPruneEmpty458_RemoveFails(t *testing.T) {
	pruneEmpty("")
	skipIfRoot458(t)
	parent := filepath.Join(t.TempDir(), "parent")
	empty := filepath.Join(parent, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	readOnly458(t, parent)
	pruneEmpty(empty) // logs a warning, must not panic
	if _, err := os.Stat(empty); err != nil {
		t.Errorf("empty dir should survive failed prune: %v", err)
	}
}

func TestPathHelpers458(t *testing.T) {
	if got := orderedUniquePaths([]string{"/a", "", "/a", "/b"}); len(got) != 2 {
		t.Errorf("orderedUniquePaths = %v", got)
	}
	if got := normalizeUniquePaths([]string{"/b", " ", "/a", "/b"}); len(got) != 2 || got[0] != "/a" {
		t.Errorf("normalizeUniquePaths = %v", got)
	}
	if hasPreservedDescendant("", []string{"/a"}) {
		t.Error("empty path has no descendants")
	}
	if err := removeDatabaseArtifacts(""); err != nil {
		t.Error(err)
	}
	if err := removeAll(""); err != nil {
		t.Error(err)
	}
}

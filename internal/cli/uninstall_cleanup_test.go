package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

func TestRunUninstallCleanupPreservesNestedBackupRoot(t *testing.T) {
	t.Parallel()

	tmpRoot := t.TempDir()
	configDir := filepath.Join(tmpRoot, "config")
	backupRoot := filepath.Join(configDir, "backups")
	pluginDir := filepath.Join(tmpRoot, "plugin-ui")
	cacheRoot := filepath.Join(tmpRoot, "cache")
	hybridDir := filepath.Join(tmpRoot, "hybrid")
	stagingOverride := filepath.Join(tmpRoot, "staging-override")
	snapshotOverride := filepath.Join(tmpRoot, "snapshot-override", "vault.db")

	for _, dir := range []string{configDir, backupRoot, pluginDir, cacheRoot, hybridDir, stagingOverride, filepath.Dir(snapshotOverride)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
	}

	dbPath := filepath.Join(configDir, "vault.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if _, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "local",
		Type:   "local",
		Config: `{"path":"` + backupRoot + `"}`,
	}); err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}
	if err := database.SetSetting("snapshot_path_override", snapshotOverride); err != nil {
		t.Fatalf("SetSetting(snapshot_path_override): %v", err)
	}
	if err := database.SetSetting("staging_dir_override", stagingOverride); err != nil {
		t.Fatalf("SetSetting(staging_dir_override): %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("database.Close: %v", err)
	}

	managedFiles := []string{
		filepath.Join(configDir, "vault.cfg"),
		filepath.Join(configDir, "vault.key"),
		dbPath + "-wal",
		dbPath + "-shm",
		filepath.Join(configDir, "vault-legacy.tgz"),
		filepath.Join(configDir, "vault-2026.03.00.tgz"),
		filepath.Join(configDir, "vault-2026.03.00.txz"),
		filepath.Join(tmpRoot, "vault.log"),
		filepath.Join(tmpRoot, "vault.pid"),
		filepath.Join(tmpRoot, "vault-bin"),
		filepath.Join(tmpRoot, "rc.vault"),
		filepath.Join(hybridDir, "vault.db"),
		snapshotOverride,
		filepath.Join(cacheRoot, ".vault", "vault.db"),
	}
	for _, filePath := range managedFiles {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", filepath.Dir(filePath), err)
		}
		if err := os.WriteFile(filePath, []byte("trace"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", filePath, err)
		}
	}

	if err := os.WriteFile(filepath.Join(backupRoot, "keep.tar"), []byte("backup"), 0o644); err != nil {
		t.Fatalf("WriteFile backup: %v", err)
	}

	for _, stageBase := range []string{
		filepath.Join(backupRoot, ".vault-stage"),
		filepath.Join(stagingOverride, ".vault-stage"),
		filepath.Join(cacheRoot, ".vault-stage"),
		filepath.Join(cacheRoot, ".vault-tmp"),
	} {
		staleDir := filepath.Join(stageBase, "stale")
		if err := os.MkdirAll(staleDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", staleDir, err)
		}
	}

	cfg := uninstallCleanupConfig{
		DBPath:            dbPath,
		ConfigDir:         configDir,
		BinaryPath:        filepath.Join(tmpRoot, "vault-bin"),
		RCScriptPath:      filepath.Join(tmpRoot, "rc.vault"),
		PluginDir:         pluginDir,
		LogPath:           filepath.Join(tmpRoot, "vault.log"),
		PIDFile:           filepath.Join(tmpRoot, "vault.pid"),
		HybridWorkingDir:  hybridDir,
		DefaultSnapshotDB: filepath.Join(cacheRoot, ".vault", "vault.db"),
		CachePaths:        []string{cacheRoot},
	}

	if err := runUninstallCleanup(cfg); err != nil {
		t.Fatalf("runUninstallCleanup() error = %v", err)
	}

	for _, path := range []string{
		cfg.BinaryPath,
		cfg.RCScriptPath,
		cfg.PluginDir,
		cfg.LogPath,
		cfg.PIDFile,
		filepath.Join(configDir, "vault.cfg"),
		filepath.Join(configDir, "vault.key"),
		dbPath,
		dbPath + "-wal",
		dbPath + "-shm",
		filepath.Join(configDir, "vault-legacy.tgz"),
		filepath.Join(configDir, "vault-2026.03.00.tgz"),
		filepath.Join(configDir, "vault-2026.03.00.txz"),
		hybridDir,
		snapshotOverride,
		cfg.DefaultSnapshotDB,
		filepath.Join(backupRoot, ".vault-stage"),
		filepath.Join(stagingOverride, ".vault-stage"),
		filepath.Join(cacheRoot, ".vault-stage"),
		filepath.Join(cacheRoot, ".vault-tmp"),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("expected %s to be removed, got stat error %v", path, statErr)
		}
	}

	if _, err := os.Stat(filepath.Join(backupRoot, "keep.tar")); err != nil {
		t.Fatalf("expected backup payload to remain: %v", err)
	}
	if _, err := os.Stat(backupRoot); err != nil {
		t.Fatalf("expected backup root to remain: %v", err)
	}
	if _, err := os.Stat(configDir); err != nil {
		t.Fatalf("expected config dir ancestor of preserved backups to remain: %v", err)
	}
}

func TestRunUninstallCleanupFallsBackSafelyWithoutDatabase(t *testing.T) {
	t.Parallel()

	tmpRoot := t.TempDir()
	configDir := filepath.Join(tmpRoot, "config")
	backupRoot := filepath.Join(configDir, "backups")
	if err := os.MkdirAll(backupRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", backupRoot, err)
	}

	for _, path := range []string{
		filepath.Join(configDir, "vault.cfg"),
		filepath.Join(configDir, "vault.key"),
		filepath.Join(configDir, "vault.db"),
		filepath.Join(configDir, "vault.db-wal"),
		filepath.Join(configDir, "vault.db-shm"),
		filepath.Join(configDir, "vault-legacy.tgz"),
		filepath.Join(configDir, "vault-2026.03.00.tgz"),
		filepath.Join(configDir, "vault-2026.03.00.txz"),
		filepath.Join(backupRoot, "keep.tar"),
	} {
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}

	cfg := uninstallCleanupConfig{
		DBPath:            filepath.Join(configDir, "vault.db"),
		ConfigDir:         configDir,
		DefaultSnapshotDB: filepath.Join(tmpRoot, "snapshot", "vault.db"),
		CachePaths:        []string{filepath.Join(tmpRoot, "cache")},
	}

	if err := runUninstallCleanup(cfg); err != nil {
		t.Fatalf("runUninstallCleanup() error = %v", err)
	}

	for _, path := range []string{
		filepath.Join(configDir, "vault.cfg"),
		filepath.Join(configDir, "vault.key"),
		filepath.Join(configDir, "vault.db"),
		filepath.Join(configDir, "vault.db-wal"),
		filepath.Join(configDir, "vault.db-shm"),
		filepath.Join(configDir, "vault-legacy.tgz"),
		filepath.Join(configDir, "vault-2026.03.00.tgz"),
		filepath.Join(configDir, "vault-2026.03.00.txz"),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("expected %s to be removed, got stat error %v", path, statErr)
		}
	}

	if _, err := os.Stat(filepath.Join(backupRoot, "keep.tar")); err != nil {
		t.Fatalf("expected unknown backup content to remain during fallback cleanup: %v", err)
	}
}

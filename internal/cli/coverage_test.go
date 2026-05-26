package cli

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// ---------------------------------------------------------------------------
// root.go — SetVersion + Execute trivial setters
// ---------------------------------------------------------------------------

// TestSetVersion stores a value and verifies the package-level version
// variable reflects it.
func TestSetVersion(t *testing.T) {
	// not parallel: mutates package-level state.
	prev := version
	t.Cleanup(func() { version = prev })

	SetVersion("v1.2.3")
	if version != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", version)
	}
}

// TestExecute_HelpFlag invokes Execute with --help to verify the rootCmd
// dispatches without error. We capture stdout to keep the test quiet.
func TestExecute_HelpFlag(t *testing.T) {
	// not parallel: rewrites os.Args + rootCmd args.
	prevArgs := os.Args
	t.Cleanup(func() { os.Args = prevArgs })
	os.Args = []string{"vault", "--help"}

	// Cobra by default prints help to stdout; suppress by redirecting.
	origOut := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() {
		_ = w.Close()
		os.Stdout = origOut
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
	}()
	// rootCmd may also use its own SetOut buffer — set it just in case.
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"--help"})

	if err := Execute(); err != nil {
		t.Errorf("Execute() returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// cache.go — checkCacheMount wrapper
// ---------------------------------------------------------------------------

// TestCheckCacheMount_DefaultArgs invokes the production wrapper to ensure
// it forwards to checkCacheMountWith with the default mount-check. A
// nonexistent path always returns cacheNotExist regardless of the real
// /proc/self/mountinfo state, so the assertion is platform-stable.
func TestCheckCacheMount_DefaultArgs(t *testing.T) {
	t.Parallel()
	status := checkCacheMount("/definitely/does/not/exist/cache-mount")
	if status != cacheNotExist {
		t.Errorf("got %d, want cacheNotExist", status)
	}
}

// ---------------------------------------------------------------------------
// dedup.go — loadServerKeyAtPath
// ---------------------------------------------------------------------------

// TestLoadServerKeyAtPath_MissingFile drives the io.ReadFile error branch.
func TestLoadServerKeyAtPath_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := loadServerKeyAtPath(filepath.Join(t.TempDir(), "missing.key"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// TestLoadServerKeyAtPath_WrongSize drives the size-mismatch branch.
func TestLoadServerKeyAtPath_WrongSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "vault.key")
	if err := os.WriteFile(keyPath, []byte("too-short"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	_, err := loadServerKeyAtPath(keyPath)
	if err == nil {
		t.Fatal("expected size-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected size") {
		t.Errorf("error = %v, want 'unexpected size' substring", err)
	}
}

// TestLoadServerKeyAtPath_ValidKey exercises the happy path.
func TestLoadServerKeyAtPath_ValidKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "vault.key")
	if err := os.WriteFile(keyPath, make([]byte, crypto.ServerKeySize), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	key, err := loadServerKeyAtPath(keyPath)
	if err != nil {
		t.Fatalf("loadServerKeyAtPath err = %v", err)
	}
	if len(key) != crypto.ServerKeySize {
		t.Errorf("key len = %d, want %d", len(key), crypto.ServerKeySize)
	}
}

// ---------------------------------------------------------------------------
// dedup.go — openDedupContext: error paths + happy path
// ---------------------------------------------------------------------------

// TestOpenDedupContext_MissingDestID drives the --dest required check.
func TestOpenDedupContext_MissingDestID(t *testing.T) {
	// not parallel: writes the dedup-test sentinel destID.
	prev := dedupDestID
	t.Cleanup(func() { dedupDestID = prev })
	dedupDestID = 0

	_, _, err := openDedupContext("/some/path/vault.db", "/some/path/vault.key")
	if err == nil {
		t.Fatal("expected error for missing --dest, got nil")
	}
	if !strings.Contains(err.Error(), "--dest is required") {
		t.Errorf("error = %v, want '--dest is required' substring", err)
	}
}

// TestOpenDedupContext_BadDBPath drives the db.Open failure.
func TestOpenDedupContext_BadDBPath(t *testing.T) {
	// not parallel: dedupDestID is package-global.
	prev := dedupDestID
	t.Cleanup(func() { dedupDestID = prev })
	dedupDestID = 1

	// Empty path is invalid for db.Open.
	_, _, err := openDedupContext("", "")
	if err == nil {
		t.Fatal("expected open-db error, got nil")
	}
}

// TestOpenDedupContext_DestNotDedupEnabled drives the dedup-not-enabled
// branch using a real DB + valid storage destination row that has
// dedup_enabled = false.
func TestOpenDedupContext_DestNotDedupEnabled(t *testing.T) {
	prev := dedupDestID
	t.Cleanup(func() { dedupDestID = prev })

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vault.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	storageDir := t.TempDir()
	cfg := `{"path":"` + storageDir + `"}`
	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "non-dedup", Type: "local", Config: cfg, DedupEnabled: false,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	_ = database.Close()
	dedupDestID = destID

	_, _, err = openDedupContext(dbPath, filepath.Join(dir, "missing.key"))
	if err == nil {
		t.Fatal("expected not-dedup-enabled error, got nil")
	}
	if !strings.Contains(err.Error(), "not dedup-enabled") {
		t.Errorf("error = %v, want 'not dedup-enabled' substring", err)
	}
}

// TestOpenDedupContext_HappyPath constructs a valid dedup-enabled
// destination, writes a server key to disk, initialises the repo on
// storage, and verifies openDedupContext succeeds end-to-end.
func TestOpenDedupContext_HappyPath(t *testing.T) {
	prev := dedupDestID
	t.Cleanup(func() { dedupDestID = prev })

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vault.db")
	keyPath := filepath.Join(dir, "vault.key")

	// Write a valid 32-byte server key.
	serverKey := bytes.Repeat([]byte{0x42}, crypto.ServerKeySize)
	if err := os.WriteFile(keyPath, serverKey, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	storageDir := t.TempDir()
	cfg := `{"path":"` + storageDir + `"}`
	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "dedup-ok", Type: "local", Config: cfg, DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	// Initialise the dedup repo on storage so OpenRepo succeeds.
	adapter, err := storage.NewAdapter("local", cfg)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if _, err := dedup.InitRepo(database, adapter, destID, serverKey); err != nil {
		storage.CloseAdapter(adapter)
		t.Fatalf("init repo: %v", err)
	}
	storage.CloseAdapter(adapter)
	_ = database.Close()

	dedupDestID = destID
	ctx, cleanup, err := openDedupContext(dbPath, keyPath)
	if err != nil {
		t.Fatalf("openDedupContext err = %v", err)
	}
	defer cleanup()
	if ctx == nil || ctx.repo == nil || ctx.db == nil || ctx.adapter == nil {
		t.Fatal("context fields nil")
	}
	if ctx.destID != destID {
		t.Errorf("destID = %d, want %d", ctx.destID, destID)
	}
}

// ---------------------------------------------------------------------------
// dedup.go — runDedupRepair + runDedupGC against a fresh empty repo
// ---------------------------------------------------------------------------

// dedupTestEnv wraps the boilerplate of creating a dedup-enabled
// destination, initialising the repo, and writing a server key file.
// Returns the dbPath, keyPath, and destID; closes the DB so the CLI
// code paths can re-open it.
func dedupTestEnv(t *testing.T) (dbPath, keyPath string, destID int64) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "vault.db")
	keyPath = filepath.Join(dir, "vault.key")
	serverKey := bytes.Repeat([]byte{0x7e}, crypto.ServerKeySize)
	if err := os.WriteFile(keyPath, serverKey, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	storageDir := t.TempDir()
	cfg := `{"path":"` + storageDir + `"}`
	destID, err = database.CreateStorageDestination(db.StorageDestination{
		Name: "dedup-env", Type: "local", Config: cfg, DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	adapter, err := storage.NewAdapter("local", cfg)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if _, err := dedup.InitRepo(database, adapter, destID, serverKey); err != nil {
		storage.CloseAdapter(adapter)
		t.Fatalf("init repo: %v", err)
	}
	storage.CloseAdapter(adapter)
	if err := database.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	return dbPath, keyPath, destID
}

// TestRunDedupRepair_Empty runs the repair against a freshly initialised
// (empty) dedup repo whose _vault/index directory was pre-created.
// Should succeed with zero packs/chunks since there are no JSONL index
// blobs on storage yet.
func TestRunDedupRepair_Empty(t *testing.T) {
	prev := dedupDestID
	prevDB := dedupRepairDBVal
	prevKey := dedupRepairKey
	t.Cleanup(func() {
		dedupDestID = prev
		dedupRepairDBVal = prevDB
		dedupRepairKey = prevKey
	})

	dbPath, keyPath, destID := dedupTestEnv(t)
	dedupDestID = destID
	dedupRepairDBVal = dbPath
	dedupRepairKey = keyPath

	// Pre-create _vault/index so the index lister has a real (empty) dir
	// to read instead of erroring out on a missing directory.
	storageDir := storageRootFromDB(t, dbPath, destID)
	if err := os.MkdirAll(filepath.Join(storageDir, "_vault", "index"), 0o755); err != nil {
		t.Fatalf("mkdir index dir: %v", err)
	}

	if err := runDedupRepair(nil, nil); err != nil {
		t.Fatalf("runDedupRepair: %v", err)
	}
}

// storageRootFromDB reads back the destination row to find its local
// adapter's path, since dedupTestEnv allocates a fresh temp dir we don't
// otherwise have a handle to.
func storageRootFromDB(t *testing.T, dbPath string, destID int64) string {
	t.Helper()
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	dest, err := database.GetStorageDestination(destID)
	if err != nil {
		t.Fatalf("get dest: %v", err)
	}
	var cfg struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(dest.Config), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return cfg.Path
}

// TestRunDedupGC_Empty runs garbage collection against a freshly initialised
// empty repo with no restore points; should succeed as a no-op.
func TestRunDedupGC_Empty(t *testing.T) {
	prev := dedupDestID
	prevDB := dedupGCDBVal
	prevKey := dedupGCKey
	t.Cleanup(func() {
		dedupDestID = prev
		dedupGCDBVal = prevDB
		dedupGCKey = prevKey
	})

	dbPath, keyPath, destID := dedupTestEnv(t)
	dedupDestID = destID
	dedupGCDBVal = dbPath
	dedupGCKey = keyPath

	if err := runDedupGC(nil, nil); err != nil {
		t.Fatalf("runDedupGC: %v", err)
	}
}

// TestRunDedupRepair_BadDest exercises the openDedupContext error path.
func TestRunDedupRepair_BadDest(t *testing.T) {
	prev := dedupDestID
	t.Cleanup(func() { dedupDestID = prev })
	dedupDestID = 0 // missing --dest

	if err := runDedupRepair(nil, nil); err == nil {
		t.Fatal("expected error for missing --dest, got nil")
	}
}

// TestRunDedupGC_BadDest exercises the openDedupContext error path.
func TestRunDedupGC_BadDest(t *testing.T) {
	prev := dedupDestID
	t.Cleanup(func() { dedupDestID = prev })
	dedupDestID = 0

	if err := runDedupGC(nil, nil); err == nil {
		t.Fatal("expected error for missing --dest, got nil")
	}
}

// ---------------------------------------------------------------------------
// dedup.go — collectLiveManifestIDsForCLI
// ---------------------------------------------------------------------------

// TestCollectLiveManifestIDsForCLI_EmptyDB returns no manifest IDs when the
// database has zero restore points pointing at the dest.
func TestCollectLiveManifestIDsForCLI_EmptyDB(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vault.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	storageDir := t.TempDir()
	cfg := `{"path":"` + storageDir + `"}`
	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "empty-dest", Type: "local", Config: cfg, DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	serverKey := bytes.Repeat([]byte{0x33}, crypto.ServerKeySize)
	adapter, err := storage.NewAdapter("local", cfg)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	t.Cleanup(func() { storage.CloseAdapter(adapter) })
	repo, err := dedup.InitRepo(database, adapter, destID, serverKey)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}

	ids, err := collectLiveManifestIDsForCLI(repo, database, destID)
	if err != nil {
		t.Fatalf("collectLiveManifestIDsForCLI: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty IDs, got %d", len(ids))
	}
}

// TestCollectLiveManifestIDsForCLI_WithRestorePoint seeds one restore
// point that has a 32-byte manifest_id; collectLiveManifestIDsForCLI
// surfaces it via the addTop branch.
func TestCollectLiveManifestIDsForCLI_WithRestorePoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vault.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	storageDir := t.TempDir()
	cfg := `{"path":"` + storageDir + `"}`
	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "rp-dest", Type: "local", Config: cfg, DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	serverKey := bytes.Repeat([]byte{0x99}, crypto.ServerKeySize)
	adapter, err := storage.NewAdapter("local", cfg)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	t.Cleanup(func() { storage.CloseAdapter(adapter) })
	repo, err := dedup.InitRepo(database, adapter, destID, serverKey)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}

	jobID, err := database.CreateJob(db.Job{
		Name:          "manifest-job",
		StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Insert a 32-byte manifest_id directly via raw SQL.
	fakeManifest := bytes.Repeat([]byte{0xab}, 32)
	rpID, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "rp1", Metadata: "{}",
	})
	if err != nil {
		t.Fatalf("create restore point: %v", err)
	}
	if _, err := database.Exec(
		`UPDATE restore_points SET manifest_id = ? WHERE id = ?`,
		fakeManifest, rpID,
	); err != nil {
		t.Fatalf("update manifest_id: %v", err)
	}

	ids, err := collectLiveManifestIDsForCLI(repo, database, destID)
	if err != nil {
		// engine.WalkManifestClosure will fail to read the fake manifest;
		// the function logs a warning and still includes the top-level ID.
		t.Logf("collectLiveManifestIDsForCLI err = %v (expected — fake manifest is unreadable)", err)
	}
	// We expect at least one ID (the top-level, via the warning-fallback
	// path that adds the top even when WalkManifestClosure errors).
	if len(ids) == 0 {
		t.Error("expected at least one manifest ID, got 0")
	}
}

// TestCollectLiveManifestIDsForCLI_WithItemManifestsMetadata seeds the
// im (item_manifests) branch of metadata decoding.
func TestCollectLiveManifestIDsForCLI_WithItemManifestsMetadata(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vault.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	storageDir := t.TempDir()
	cfg := `{"path":"` + storageDir + `"}`
	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "im-dest", Type: "local", Config: cfg, DedupEnabled: true,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	serverKey := bytes.Repeat([]byte{0x66}, crypto.ServerKeySize)
	adapter, err := storage.NewAdapter("local", cfg)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	t.Cleanup(func() { storage.CloseAdapter(adapter) })
	repo, err := dedup.InitRepo(database, adapter, destID, serverKey)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}

	jobID, err := database.CreateJob(db.Job{
		Name:          "im-job",
		StorageDestID: destID,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	// Metadata with item_manifests of varying validity.
	itemHex := hex.EncodeToString(bytes.Repeat([]byte{0x11}, 32))
	metadata := `{"item_manifests":{"good":"` + itemHex + `","short":"abcd","junk":not-string}}`
	// Last entry intentionally invalid JSON to exercise the json.Unmarshal err path.
	// metadata as written is malformed; CreateRestorePoint accepts arbitrary
	// strings.
	if _, err := database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "rp1", Metadata: metadata,
	}); err != nil {
		t.Fatalf("create rp: %v", err)
	}

	ids, _ := collectLiveManifestIDsForCLI(repo, database, destID)
	// We just want to ensure no panic; the result can be empty.
	_ = ids
}

// ---------------------------------------------------------------------------
// uninstall_cleanup.go — defaultUninstallCleanupConfig + helper coverage
// ---------------------------------------------------------------------------

// TestDefaultUninstallCleanupConfig populates a sensible default config.
func TestDefaultUninstallCleanupConfig(t *testing.T) {
	t.Parallel()
	cfg := defaultUninstallCleanupConfig()
	if cfg.DBPath == "" || cfg.ConfigDir == "" || cfg.BinaryPath == "" {
		t.Errorf("default config has empty fields: %+v", cfg)
	}
	if cfg.DefaultSnapshotDB == "" {
		t.Error("DefaultSnapshotDB should be set")
	}
}

// TestRemoveDirContentsExceptPreserved_MissingDir handles the os.ErrNotExist
// branch.
func TestRemoveDirContentsExceptPreserved_MissingDir(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := removeDirContentsExceptPreserved(missing, nil); err != nil {
		t.Errorf("expected nil error for missing dir, got %v", err)
	}
}

// TestRemoveDirContentsExceptPreserved_EmptyDir is a no-op.
func TestRemoveDirContentsExceptPreserved_EmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := removeDirContentsExceptPreserved(dir, nil); err != nil {
		t.Errorf("expected nil error for empty dir, got %v", err)
	}
}

// TestRemoveDirContentsExceptPreserved_WithExactPreserveRoot — child is
// itself a preserve root and must remain untouched.
func TestRemoveDirContentsExceptPreserved_WithExactPreserveRoot(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	preserved := filepath.Join(parent, "keep-me")
	if err := os.MkdirAll(preserved, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(preserved, "data"), []byte("x"), 0o644); err != nil {
		t.Fatalf("writefile: %v", err)
	}

	roots := []string{normalizePath(preserved)}
	if err := removeDirContentsExceptPreserved(parent, roots); err != nil {
		t.Fatalf("removeDirContentsExceptPreserved: %v", err)
	}
	if _, err := os.Stat(preserved); err != nil {
		t.Errorf("expected preserved dir to remain: %v", err)
	}
}

// TestRemoveDirContentsExceptPreserved_WithDescendant exercises the
// recursion branch where the child is an ancestor of a preserve root.
func TestRemoveDirContentsExceptPreserved_WithDescendant(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	ancestor := filepath.Join(parent, "branch")
	descendant := filepath.Join(ancestor, "keep")
	if err := os.MkdirAll(descendant, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A sibling that should be removed.
	siblingDir := filepath.Join(ancestor, "remove-me")
	if err := os.MkdirAll(siblingDir, 0o755); err != nil {
		t.Fatalf("mkdir sibling: %v", err)
	}
	if err := os.WriteFile(filepath.Join(siblingDir, "x.bin"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	roots := []string{normalizePath(descendant)}
	if err := removeDirContentsExceptPreserved(parent, roots); err != nil {
		t.Fatalf("removeDirContentsExceptPreserved: %v", err)
	}
	if _, err := os.Stat(descendant); err != nil {
		t.Errorf("descendant should remain: %v", err)
	}
	if _, err := os.Stat(siblingDir); !os.IsNotExist(err) {
		t.Errorf("sibling should be removed: %v", err)
	}
}

// TestPruneEmpty_NonEmptyDirLeftAlone covers the early-return branch
// where the directory has entries.
func TestPruneEmpty_NonEmptyDirLeftAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	pruneEmpty(dir)
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("expected dir to remain (non-empty), got %v", err)
	}
}

// TestPruneEmpty_EmptyPath returns silently on the trim-empty branch.
func TestPruneEmpty_EmptyPath(t *testing.T) {
	t.Parallel()
	pruneEmpty("")    // empty input → early return; must not panic.
	pruneEmpty("   ") // whitespace → normalizePath returns "" → early return.
}

// TestCleanupConfigDir_EmptyConfigDir exercises the early-return branch
// when ConfigDir is empty.
func TestCleanupConfigDir_EmptyConfigDir(t *testing.T) {
	t.Parallel()
	cfg := uninstallCleanupConfig{ConfigDir: ""}
	state := uninstallCleanupState{Confident: true}
	if err := cleanupConfigDir(cfg, state); err != nil {
		t.Errorf("expected nil error for empty config dir, got %v", err)
	}
}

// TestCleanupConfigDir_ExactPreserveRoot — config dir IS the preserve root.
func TestCleanupConfigDir_ExactPreserveRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Write a file so pruneEmpty leaves dir alone.
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := uninstallCleanupConfig{ConfigDir: dir}
	state := uninstallCleanupState{
		Confident:     true,
		PreserveRoots: []string{normalizePath(dir)},
	}
	if err := cleanupConfigDir(cfg, state); err != nil {
		t.Errorf("cleanupConfigDir: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("expected config dir to remain (exact preserve), got %v", err)
	}
}

// TestCleanupConfigDir_NoPreservedDescendant — removeAll is called.
func TestCleanupConfigDir_NoPreservedDescendant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a child to ensure removeAll has something to remove.
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := uninstallCleanupConfig{ConfigDir: dir}
	state := uninstallCleanupState{
		Confident:     true,
		PreserveRoots: []string{"/totally/unrelated/path"},
	}
	if err := cleanupConfigDir(cfg, state); err != nil {
		t.Errorf("cleanupConfigDir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected dir removed, got %v", err)
	}
}

// TestNormalizePath_EmptyString returns empty for whitespace-only input.
func TestNormalizePath_EmptyString(t *testing.T) {
	t.Parallel()
	if got := normalizePath(""); got != "" {
		t.Errorf("normalizePath('') = %q, want ''", got)
	}
	if got := normalizePath("   "); got != "" {
		t.Errorf("normalizePath('   ') = %q, want ''", got)
	}
}

// TestRemoveAll_NonexistentPath handles the ErrNotExist branch as
// a silent no-op.
func TestRemoveAll_NonexistentPath(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := removeAll(missing); err != nil {
		t.Errorf("expected nil for missing path, got %v", err)
	}
}

// TestRemoveAll_EmptyPath returns nil silently.
func TestRemoveAll_EmptyPath(t *testing.T) {
	t.Parallel()
	if err := removeAll(""); err != nil {
		t.Errorf("expected nil for empty path, got %v", err)
	}
}

// TestRemoveDatabaseArtifacts_EmptyPath silent no-op.
func TestRemoveDatabaseArtifacts_EmptyPath(t *testing.T) {
	t.Parallel()
	if err := removeDatabaseArtifacts(""); err != nil {
		t.Errorf("expected nil for empty path, got %v", err)
	}
}

// TestRemoveDatabaseArtifacts_WithAllSidecars covers the wal/shm/journal
// cleanup branches.
func TestRemoveDatabaseArtifacts_WithAllSidecars(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vault.db")
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.WriteFile(dbPath+suffix, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", suffix, err)
		}
	}
	if err := removeDatabaseArtifacts(dbPath); err != nil {
		t.Errorf("removeDatabaseArtifacts: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Stat(dbPath + suffix); !os.IsNotExist(err) {
			t.Errorf("expected %s removed", dbPath+suffix)
		}
	}
}

// TestHasPreservedDescendant covers the false-return branch with no
// matching descendant.
func TestHasPreservedDescendant_NoMatch(t *testing.T) {
	t.Parallel()
	if hasPreservedDescendant("/a/b", []string{"/c/d"}) {
		t.Error("expected false for unrelated paths")
	}
	if hasPreservedDescendant("", []string{"/a/b"}) {
		t.Error("expected false for empty path")
	}
}

// TestIsExactPreserveRoot covers the true-match and false-match branches.
func TestIsExactPreserveRoot_Cases(t *testing.T) {
	t.Parallel()
	if !isExactPreserveRoot("/a/b", []string{"/a/b"}) {
		t.Error("expected exact match true")
	}
	if isExactPreserveRoot("/a/b", []string{"/c/d"}) {
		t.Error("expected false for mismatch")
	}
}

// TestLocalStorageRoots_NonLocalSkipped ensures the type != "local" branch
// is exercised and the path is dropped.
func TestLocalStorageRoots_NonLocalSkipped(t *testing.T) {
	t.Parallel()
	dests := []db.StorageDestination{
		{Type: "sftp", Config: `{"path":"/should-not-appear"}`},
		{Type: "local", Config: `{"path":"/keep-me"}`},
		{Type: "local", Config: `not-json-at-all`}, // unmarshal fails — skipped.
		{Type: "local", Config: `{"path":""}`},     // empty path — skipped.
	}
	roots := localStorageRoots(dests)
	if len(roots) != 1 {
		t.Fatalf("expected exactly 1 root, got %d (%v)", len(roots), roots)
	}
	if roots[0] != "/keep-me" {
		t.Errorf("root = %q, want /keep-me", roots[0])
	}
}

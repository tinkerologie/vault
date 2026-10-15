package cli

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/storage"
)

// captureStd458 redirects os.Stdout and os.Stderr into temp files while fn
// runs and returns what was written. Files (not pipes) avoid deadlocks on
// large output.
func captureStd458(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	outF, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	errF, err := os.Create(filepath.Join(dir, "err"))
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outF, errF
	func() {
		defer func() { os.Stdout, os.Stderr = origOut, origErr }()
		fn()
	}()
	_ = outF.Close()
	_ = errF.Close()
	o, _ := os.ReadFile(outF.Name())
	e, _ := os.ReadFile(errF.Name())
	return string(o), string(e)
}

// resetDedupFlags458 snapshots the dedup flag globals and restores them.
func resetDedupFlags458(t *testing.T) {
	t.Helper()
	prevID, prevRDB, prevGDB, prevRKey, prevGKey := dedupDestID, dedupRepairDBVal, dedupGCDBVal, dedupRepairKey, dedupGCKey
	t.Cleanup(func() {
		dedupDestID, dedupRepairDBVal, dedupGCDBVal, dedupRepairKey, dedupGCKey = prevID, prevRDB, prevGDB, prevRKey, prevGKey
	})
}

// seedDedupRepo458 writes one live manifest (referencing one data chunk)
// and one dead pack into the destination, plus a restore point that keeps
// the manifest alive. Returns the live manifest ID.
func seedDedupRepo458(t *testing.T, dbPath, keyPath string, destID int64, withMissing bool) dedup.ID {
	t.Helper()
	serverKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	dest, err := database.GetStorageDestination(destID)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.CloseAdapter(adapter)
	repo, err := dedup.OpenRepo(database, adapter, destID, serverKey)
	if err != nil {
		t.Fatal(err)
	}

	// Dead pack: a chunk nothing references, flushed on its own.
	if _, err := repo.Put(bytes.Repeat([]byte("dead-chunk-"), 512)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	dataID, err := repo.Put(bytes.Repeat([]byte("live-chunk-"), 512))
	if err != nil {
		t.Fatal(err)
	}
	mID, err := repo.PutManifest("item", dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		"file.txt": {Mode: 0o644, Size: 5632, Chunks: []dedup.ID{dataID}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	jobID, err := database.CreateJob(db.Job{Name: "j", StorageDestID: destID})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]any{
		"dup":    hex.EncodeToString(mID[:]),
		"empty":  "",
		"badhex": "zz",
		"short":  "abcd",
		"notstr": 5,
	}
	if withMissing {
		// A manifest ID that was never stored: unreadable during the walk.
		items["missing"] = hex.EncodeToString(bytes.Repeat([]byte{0xab}, 32))
	}
	meta, err := json.Marshal(map[string]any{"item_manifests": items})
	if err != nil {
		t.Fatal(err)
	}
	rpID, err := database.CreateRestorePoint(db.RestorePoint{JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "x", Metadata: string(meta)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE restore_points SET manifest_id = ? WHERE id = ?`, mID[:], rpID); err != nil {
		t.Fatal(err)
	}
	return mID
}

func TestRunDedupGC458_WithLiveAndDeadData(t *testing.T) {
	resetDedupFlags458(t)
	dbPath, keyPath, destID := dedupTestEnv(t)
	mID := seedDedupRepo458(t, dbPath, keyPath, destID, false)

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("dedup_compaction_min_dead_ratio", "not-a-ratio"); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	dedupDestID, dedupGCDBVal, dedupGCKey = destID, dbPath, "" // default key beside --db
	var runErr error
	stdout, stderr := captureStd458(t, func() { runErr = runDedupGC(nil, nil) })
	if runErr != nil {
		t.Fatalf("runDedupGC: %v", runErr)
	}
	if !strings.Contains(stdout, "dedup gc: freed 1 packs") {
		t.Errorf("stdout = %q, want one freed pack", stdout)
	}
	if !strings.Contains(stderr, "invalid dedup_compaction_min_dead_ratio") {
		t.Errorf("stderr missing ratio warning: %q", stderr)
	}

	// The live manifest must still be readable after GC.
	serverKey, _ := os.ReadFile(keyPath)
	d2, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d2.Close() }()
	dest, _ := d2.GetStorageDestination(destID)
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.CloseAdapter(adapter)
	repo, err := dedup.OpenRepo(d2, adapter, destID, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetManifest(mID); err != nil {
		t.Errorf("live manifest lost after GC: %v", err)
	}
}

func TestRunDedupGC458_UnreadableManifestFailsGC(t *testing.T) {
	resetDedupFlags458(t)
	dbPath, keyPath, destID := dedupTestEnv(t)
	seedDedupRepo458(t, dbPath, keyPath, destID, true)
	dedupDestID, dedupGCDBVal, dedupGCKey = destID, dbPath, keyPath

	var runErr error
	_, stderr := captureStd458(t, func() { runErr = runDedupGC(nil, nil) })
	if !strings.Contains(stderr, "skipping unreadable manifest") {
		t.Errorf("stderr missing unreadable-manifest warning: %q", stderr)
	}
	// GC refuses to sweep when a live manifest cannot be loaded.
	if runErr == nil || !strings.Contains(runErr.Error(), "run gc") {
		t.Fatalf("err = %v, want run gc error", runErr)
	}
}

func TestRunDedupGC458_CollectError(t *testing.T) {
	resetDedupFlags458(t)
	dbPath, keyPath, destID := dedupTestEnv(t)
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Replace restore_points with a table lacking manifest_id so the live
	// manifest query fails.
	for _, q := range []string{
		`DROP TABLE restore_points`,
		`CREATE TABLE restore_points (id INTEGER PRIMARY KEY, job_id INTEGER)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.Close()
	dedupDestID, dedupGCDBVal, dedupGCKey = destID, dbPath, keyPath

	var runErr error
	_, _ = captureStd458(t, func() { runErr = runDedupGC(nil, nil) })
	if runErr == nil || !strings.Contains(runErr.Error(), "collect live manifest IDs") {
		t.Fatalf("err = %v, want collect error", runErr)
	}
}

func TestRunDedupRepair458_RebuildsSeededIndex(t *testing.T) {
	resetDedupFlags458(t)
	dbPath, keyPath, destID := dedupTestEnv(t)
	seedDedupRepo458(t, dbPath, keyPath, destID, false)

	// Wipe the local index so repair has to rebuild it from storage.
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DELETE FROM dedup_chunks`, `DELETE FROM dedup_packs`} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.Close()

	dedupDestID, dedupRepairDBVal, dedupRepairKey = destID, dbPath, keyPath
	var runErr error
	stdout, _ := captureStd458(t, func() { runErr = runDedupRepair(nil, nil) })
	if runErr != nil {
		t.Fatalf("runDedupRepair: %v", runErr)
	}
	if !strings.Contains(stdout, "rebuilt 2 packs") {
		t.Errorf("stdout = %q, want 2 rebuilt packs", stdout)
	}
}

func TestRunDedupRepair458_RebuildError(t *testing.T) {
	resetDedupFlags458(t)
	dbPath, keyPath, destID := dedupTestEnv(t)
	// No _vault/index directory on storage → listing fails.
	root := storageRootFromDB(t, dbPath, destID)
	_ = os.RemoveAll(filepath.Join(root, "_vault", "index"))
	if err := os.MkdirAll(filepath.Join(root, "_vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "_vault", "index"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	dedupDestID, dedupRepairDBVal, dedupRepairKey = destID, dbPath, keyPath
	err := runDedupRepair(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "rebuild dedup index") {
		t.Fatalf("err = %v, want rebuild error", err)
	}
}

func TestOpenDedupContext458_Errors(t *testing.T) {
	resetDedupFlags458(t)
	dbPath, keyPath, destID := dedupTestEnv(t)
	dir := filepath.Dir(dbPath)

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	bogusID, err := d.CreateStorageDestination(db.StorageDestination{Name: "bogus", Type: "bogus", Config: `{}`, DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	uninitID, err := d.CreateStorageDestination(db.StorageDestination{Name: "uninit", Type: "local", Config: `{"path":"` + t.TempDir() + `"}`, DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	badKey := filepath.Join(dir, "short.key")
	if err := os.WriteFile(badKey, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		dbPath string
		dest   int64
		key    string
		want   string
	}{
		{"open db", filepath.Join(dir, "missing-dir", "nested", "vault.db"), destID, keyPath, "open db"},
		{"unknown dest", dbPath, 99999, keyPath, "get destination"},
		{"missing key", dbPath, destID, filepath.Join(dir, "absent.key"), "load server key"},
		{"short key", dbPath, destID, badKey, "unexpected size"},
		{"bad adapter", dbPath, bogusID, keyPath, "build storage adapter"},
		{"uninitialised repo", dbPath, uninitID, keyPath, "open dedup repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dedupDestID = tc.dest
			_, cleanup, err := openDedupContext(tc.dbPath, tc.key)
			if cleanup != nil {
				cleanup()
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
	if crypto.ServerKeySize <= len("short") {
		t.Fatal("test assumes the short key is undersized")
	}
}

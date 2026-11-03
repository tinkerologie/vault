package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/maintlock"
	"github.com/tinkerologie/vault/internal/storage"
)

// A path a sidecar marks as a directory is removed only if it is an empty
// directory now, through the real folder prune.
func TestPruneDirectoryMarkKeepsFilesAndSymlinks(t *testing.T) {
	f := c458NewPruneFixture(t, "docs")
	dst := t.TempDir()
	settings, _ := json.Marshal(map[string]any{"path": dst})
	if _, err := f.d.AddJobItem(db.JobItem{JobID: f.jobID, ItemType: "folder", ItemName: "docs", Settings: string(settings)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "precious.db"), []byte("user"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("precious.db", filepath.Join(dst, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dst, "emptydir"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeStorageFiles(t, f.adapter, map[string]string{
		"c458-prune/1/docs/data.tar" + engine.IndexSuffix: c458IndexJSON(t,
			engine.TarIndexEntry{Path: "precious.db", IsDir: true},
			engine.TarIndexEntry{Path: "link", IsDir: true},
			engine.TarIndexEntry{Path: "emptydir", IsDir: true},
			engine.TarIndexEntry{Path: "absent", IsDir: true},
		),
		"c458-prune/2/docs/data.tar" + engine.ListingSuffix: c458IndexJSON(t),
	})

	f.r.pruneChainResurrected([]db.RestorePoint{f.base, f.child}, "docs", "", "", f.child.CreatedAt)

	for _, kept := range []string{"precious.db", "link"} {
		if _, err := os.Lstat(filepath.Join(dst, kept)); err != nil {
			t.Errorf("%s was deleted by a directory mark: %v", kept, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "emptydir")); !os.IsNotExist(err) {
		t.Errorf("empty directory should be pruned: %v", err)
	}
}

// Orphan deletion waits for nothing and deletes nothing while a backup, import
// or sync could still be adopting files.
func TestDeleteOrphansRefusesDuringWriters(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	dest := createNamedLocalDest(t, database, "orphan-race", root)
	orphan := filepath.Join(root, "stray.bin")
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	attempt := func(wantDeleted int, wantErr string) {
		t.Helper()
		n, errs := r.DeleteStorageOrphans(dest, []string{"stray.bin"})
		if n != wantDeleted {
			t.Fatalf("deleted = %d, want %d (%v)", n, wantDeleted, errs)
		}
		if wantErr != "" && (len(errs) != 1 || !strings.Contains(errs[0], wantErr)) {
			t.Fatalf("errs = %v, want %q", errs, wantErr)
		}
	}

	r.mu.Lock()
	attempt(0, "backup or restore is running")
	r.mu.Unlock()

	release := maintlock.HoldDestination(database, dest.ID)
	attempt(0, "import or replication sync")
	release()

	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("orphan deleted while a writer was active: %v", err)
	}
	attempt(1, "")
}

func TestDeleteOrphansRefusesWhileDraining(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	dest := createNamedLocalDest(t, database, "orphan-drain", root)
	r.activeMu.Lock()
	r.draining = true
	r.activeMu.Unlock()
	if n, errs := r.DeleteStorageOrphans(dest, []string{"x"}); n != 0 || len(errs) != 1 || !strings.Contains(errs[0], "shutting down") {
		t.Fatalf("got %d, %v", n, errs)
	}
}

// Files of a replicated job that has no destination recorded are not orphans,
// and a restore point that cannot be read stops the scan.
func TestOrphanScanFailsClosed(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	dest := createNamedLocalDest(t, database, "orphan-scan", root)
	jobID, err := database.CreateReplicatedJob(db.Job{Name: "[S] J", SourceID: 9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateRestorePoint(db.RestorePoint{JobRunID: authRun(t, database, jobID), JobID: jobID, BackupType: "full", StoragePath: "J/1", SourceID: 9}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "J/1"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "J/1/data"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphans, _, err := r.ScanStorageOrphans(dest)
	if err != nil || len(orphans) != 0 {
		t.Fatalf("orphans = %v, %v", orphans, err)
	}

	if _, err := database.Exec(`ALTER TABLE restore_points RENAME TO restore_points_gone`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ScanStorageOrphans(dest); err == nil {
		t.Fatal("scan succeeded with no restore point data")
	}
}

// Anything that might hide a live manifest stops GC: malformed metadata, a bad
// item manifest ID, and a nested manifest that cannot be read.
func TestCollectLiveManifestIDsFailsClosed(t *testing.T) {
	t.Parallel()
	for name, meta := range map[string]string{
		"bad json": "{",
		"bad hex":  `{"item_manifests":{"a":"zz"}}`,
		"short id": `{"item_manifests":{"a":"abcd"}}`,
		"not text": `{"item_manifests":{"a":5}}`,
		"array":    `{"item_manifests":["ab"]}`,
		"null":     `{"item_manifests":null}`,
		"string":   `{"item_manifests":"ab"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := itemManifestIDs(meta); err == nil {
				t.Fatal("malformed metadata accepted")
			}
		})
	}
	ids, err := itemManifestIDs(`{"item_manifests":{"a":"","b":"` + strings.Repeat("ab", 32) + `"},"other":1}`)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids = %x, %v", ids, err)
	}
	if ids, err := itemManifestIDs(`{}`); err != nil || len(ids) != 0 {
		t.Fatalf("no item_manifests = %x, %v", ids, err)
	}

	r, database, storageDir := setupTestRunner(t)
	r.serverKey = testServerKey()
	dest := makeDedupDest(t, database, storageDir)
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.CloseAdapter(adapter)
	repo, err := dedup.InitRepo(database, adapter, dest.ID, r.serverKey)
	if err != nil {
		t.Fatal(err)
	}
	top, err := repo.PutManifest("test", dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		"__vol__/data": {IsDir: true, Chunks: []dedup.ID{{0xAB}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	jobID, err := database.CreateJob(db.Job{Name: "gc", BackupTypeChain: "full", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	rpID, err := database.CreateRestorePoint(db.RestorePoint{JobRunID: authRun(t, database, jobID), JobID: jobID, BackupType: "full", StoragePath: "gc/1", Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetRestorePointManifestID(rpID, top[:]); err != nil {
		t.Fatal(err)
	}
	if ids, err := r.collectLiveManifestIDs(repo, dest.ID); err == nil {
		t.Fatalf("live = %x, want an error from the unreadable nested manifest", ids)
	}
	if err := database.SetRestorePointManifestID(rpID, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.collectLiveManifestIDs(repo, dest.ID); err == nil {
		t.Fatal("a manifest_id of the wrong length did not stop GC")
	}
	if _, err := database.Exec(`UPDATE restore_points SET metadata = '{', manifest_id = NULL WHERE id = ?`, rpID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.collectLiveManifestIDs(repo, dest.ID); err == nil {
		t.Fatal("malformed metadata did not stop GC")
	}
}

// Only the dataset configured on the job's own item is used, and an import does
// not take one from a manifest.
func TestZFSDatasetProvenance(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	dest := createNamedLocalDest(t, database, "zfs-dest", root)
	jobID, err := database.CreateJob(db.Job{Name: "z", BackupTypeChain: "full", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []db.JobItem{
		{JobID: jobID, ItemType: "zfs", ItemName: "set", Settings: `{"dataset":"tank/set"}`},
		{JobID: jobID, ItemType: "zfs", ItemName: "unset", Settings: `{}`},
		{JobID: jobID, ItemType: "folder", ItemName: "wrongtype", Settings: `{"dataset":"tank/no"}`},
		{JobID: jobID, ItemType: "zfs", ItemName: "bad", Settings: `{`},
	} {
		if _, err := database.AddJobItem(it); err != nil {
			t.Fatal(err)
		}
	}
	// "unset" is a local item, so its name is its dataset (TestZFSDatasetFromTrustedItemName).
	for name, want := range map[string]string{"set": "tank/set", "unset": "unset", "wrongtype": "", "missing": ""} {
		if got, err := r.configuredZFSDataset(jobID, name); err != nil || got != want {
			t.Errorf("configuredZFSDataset(%s) = %q, %v, want %q", name, got, err, want)
		}
	}
	if _, err := r.configuredZFSDataset(jobID, "bad"); err == nil {
		t.Error("malformed settings accepted")
	}

	items := buildImportJobItems(map[string]any{"items": []any{
		map[string]any{"name": "ds", "type": "zfs", "settings": map[string]any{"dataset": "tank/victim", "keep": "me"}},
		map[string]any{"name": "f", "type": "folder", "settings": map[string]any{"dataset": "stays"}},
	}})
	if len(items) != 2 || strings.Contains(items[0].Settings, "victim") || !strings.Contains(items[0].Settings, "keep") || !strings.Contains(items[1].Settings, "stays") {
		t.Fatalf("items = %+v", items)
	}
}

func authRun(t *testing.T, d *db.DB, jobID int64) int64 {
	t.Helper()
	id, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// While an import or sync is adopting files, nothing that deletes by layout or
// by reachability runs on that destination.
func TestDestructiveOperationsRefuseDuringAdoption(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	r.serverKey = testServerKey()
	dest := makeDedupDest(t, database, root)
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.CloseAdapter(adapter)
	repo, err := dedup.InitRepo(database, adapter, dest.ID, r.serverKey)
	if err != nil {
		t.Fatal(err)
	}
	dead, err := repo.Put([]byte("awaiting its restore point"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	manifest := `{"job_name":"j","run_id":1,"backup_type":"full","items_done":1}`
	if err := os.MkdirAll(filepath.Join(root, "j/run"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "j/run/manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	release := maintlock.HoldDestination(database, dest.ID)
	r.RunDedupGC(dest, "gc-during-import")
	if _, err := repo.Get(dead); err != nil {
		t.Fatalf("GC swept a pack during an import: %v", err)
	}
	if err := r.CleanupStorageDestination(dest); !errors.Is(err, ErrCleanupUnavailable) {
		t.Fatalf("destination cleanup = %v", err)
	}
	if err := r.sweepManagedRunDirs(adapter, dest, 99, "j"); !errors.Is(err, errDestinationAdopting) {
		t.Fatalf("job sweep = %v", err)
	}
	var errs []error
	r.reclaimDedupAfterJobDelete(adapter, 99, dest, &errs)
	if len(errs) != 1 || !errors.Is(errs[0], errDestinationAdopting) {
		t.Fatalf("repo reclaim errs = %v", errs)
	}
	if _, err := os.Stat(filepath.Join(root, "j/run/manifest.json")); err != nil {
		t.Fatalf("run directory deleted during an import: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "_vault/repo.json")); err != nil {
		t.Fatalf("repository deleted during an import: %v", err)
	}
	release()

	r.RunDedupGC(dest, "gc-after-import")
	if err := r.sweepManagedRunDirs(adapter, dest, 99, "j"); err != nil {
		t.Fatalf("job sweep after the import: %v", err)
	}
}

// The destination is claimed before jobs are counted, so a count can never go
// stale between the decision to remove the repository and the removal.
func TestReclaimClaimsDestinationBeforeCounting(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	dest := makeDedupDest(t, database, root)
	release := maintlock.HoldDestination(database, dest.ID)
	defer release()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	var errs []error
	r.reclaimDedupAfterJobDelete(nil, 1, dest, &errs)
	if len(errs) != 1 || !errors.Is(errs[0], errDestinationAdopting) {
		t.Fatalf("errs = %v, want a refusal before the count fails", errs)
	}
}

// An import cannot attach a verified backup to another job or destination.
func TestImportKeepsBackupWithItsOwnJob(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	dest := createNamedLocalDest(t, database, "here", root)
	other := createNamedLocalDest(t, database, "there", t.TempDir())
	victim, err := database.CreateJob(db.Job{Name: "victim", BackupTypeChain: "full", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateJob(db.Job{Name: "elsewhere", BackupTypeChain: "full", StorageDestID: other.ID}); err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{"real", "elsewhere"} {
		dir := filepath.Join(root, job, "run")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		m := `{"job_name":"` + job + `","run_id":1,"backup_type":"full","items_done":1}`
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(m), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, entry := range map[string]map[string]any{
		"renamed":    {"job_name": "victim", "storage_path": "real/run"},
		"other dest": {"job_name": "elsewhere", "storage_path": "elsewhere/run"},
	} {
		if n, err := r.ImportBackups(dest.ID, []map[string]any{entry}); err != nil || n != 0 {
			t.Errorf("%s: imported %d, %v", name, n, err)
		}
	}
	if rps, _ := database.ListRestorePoints(victim); len(rps) != 0 {
		t.Fatalf("victim gained %d restore point(s)", len(rps))
	}
	n, err := r.ImportBackups(dest.ID, []map[string]any{{"job_name": "real", "storage_path": "real/run"}})
	if err != nil || n != 1 {
		t.Fatalf("own job import = %d, %v", n, err)
	}
}

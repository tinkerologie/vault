package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/ws"
)

func otherServerKey() []byte { return bytes.Repeat([]byte{0x42}, crypto.ServerKeySize) }

// runnerWithKey builds a runner on database with the given server key.
func runnerWithKey(database *db.DB, key []byte) *Runner {
	hub := ws.NewHub()
	go hub.Run()
	return New(database, hub, key)
}

// dedupBackup creates a dedup folder job on storageDir and runs it once.
func dedupBackup(t *testing.T, r *Runner, database *db.DB, storageDir string) (db.StorageDestination, int64) {
	t.Helper()
	dest := makeDedupDest(t, database, storageDir)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	jobID, err := database.CreateJob(db.Job{Name: "escrow", StorageDestID: dest.ID, BackupTypeChain: "full", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]string{"path": src})
	if _, err := database.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(settings)}); err != nil {
		t.Fatal(err)
	}
	r.RunJob(jobID)
	runs, _ := database.GetJobRuns(jobID, 1)
	if len(runs) == 0 || runs[0].Status != "completed" {
		t.Fatalf("backup did not complete: %+v", runs)
	}
	return dest, jobID
}

func adapterFor(t *testing.T, dest db.StorageDestination) storage.Adapter {
	t.Helper()
	a, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestDedupBackupEscrowsUnderBackupPassphrase checks a backup with a
// passphrase configured writes the escrow, follows a passphrase change, and
// that a server with a different vault.key can then read the destination
// through the escrow without modifying repo.json (issue #451).
func TestDedupBackupEscrowsUnderBackupPassphrase(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	_ = database.SetSetting("encryption_passphrase", "first")
	storageDir := t.TempDir()
	r := runnerWithKey(database, testServerKey())
	dest, jobID := dedupBackup(t, r, database, storageDir)

	if _, err := os.Stat(filepath.Join(storageDir, "_vault", "master.escrow.age")); err != nil {
		t.Fatalf("backup did not write the escrow: %v", err)
	}
	if _, err := dedup.OpenRepoFromEscrow(database, adapterFor(t, dest), dest.ID, "first"); err != nil {
		t.Fatalf("escrow does not open with the backup passphrase: %v", err)
	}

	// A passphrase change is picked up by the next backup.
	_ = database.SetSetting("encryption_passphrase", "second")
	r.RunJob(jobID)
	if _, err := dedup.OpenRepoFromEscrow(database, adapterFor(t, dest), dest.ID, "second"); err != nil {
		t.Fatalf("escrow not updated after a passphrase change: %v", err)
	}

	// A different server can browse through the escrow, without writing.
	header, _ := os.ReadFile(filepath.Join(storageDir, "_vault", "repo.json"))
	other := runnerWithKey(database, otherServerKey())
	get, closeFn, err := other.OpenDedupManifests(dest)
	if err != nil {
		t.Fatalf("other server could not open the destination: %v", err)
	}
	closeFn()
	_ = get
	if after, _ := os.ReadFile(filepath.Join(storageDir, "_vault", "repo.json")); !bytes.Equal(after, header) {
		t.Fatal("reading through the escrow rewrote repo.json")
	}

	// With no passphrase to try, the mismatch is reported as such.
	_ = database.SetSetting("encryption_passphrase", "")
	if _, _, err := other.OpenDedupManifests(dest); !errors.Is(err, dedup.ErrServerKeyMismatch) {
		t.Fatalf("no passphrase: %v, want ErrServerKeyMismatch", err)
	}
	if _, _, err := other.OpenDedupManifests(dest); err == nil {
		t.Fatal("opened without any key or passphrase")
	}
}

// TestRewrapDedupKeys covers every per-destination outcome of the recovery
// step.
func TestRewrapDedupKeys(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	_ = database.SetSetting("encryption_passphrase", "pw")
	r := runnerWithKey(database, testServerKey())
	escrowed, _ := dedupBackup(t, r, database, t.TempDir())

	// A destination initialised by a server that never had a passphrase.
	bare := namedDedupDest(t, database, "bare")
	if _, err := dedup.InitRepo(database, adapterFor(t, bare), bare.ID, testServerKey()); err != nil {
		t.Fatal(err)
	}
	// A dedup destination with no backups yet.
	namedDedupDest(t, database, "empty")

	// The original server: everything already opens.
	results, err := r.RewrapDedupKeys("pw")
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses(results); got[escrowed.ID] != "ok" || got[bare.ID] != "ok" {
		t.Fatalf("same key: %+v", results)
	}

	recovered := runnerWithKey(database, otherServerKey())
	results, _ = recovered.RewrapDedupKeys("wrong")
	if got := statuses(results); got[escrowed.ID] != "locked_wrong_passphrase" || got[bare.ID] != "locked_no_escrow" {
		t.Fatalf("wrong passphrase: %+v", results)
	}
	results, _ = recovered.RewrapDedupKeys("pw")
	got := statuses(results)
	if got[escrowed.ID] != "rewrapped" || got[bare.ID] != "locked_no_escrow" {
		t.Fatalf("recovery: %+v", results)
	}
	notInit := 0
	for _, s := range got {
		if s == "not_initialised" {
			notInit++
		}
	}
	if notInit != 1 {
		t.Fatalf("expected one not_initialised destination: %+v", results)
	}
	// After the rewrap the new key opens it directly, with no passphrase.
	if _, err := dedup.OpenRepo(database, adapterFor(t, escrowed), escrowed.ID, otherServerKey()); err != nil {
		t.Fatalf("new key after rewrap: %v", err)
	}
	if results, _ = recovered.RewrapDedupKeys("pw"); statuses(results)[escrowed.ID] != "ok" {
		t.Fatalf("second recovery: %+v", results)
	}
}

func statuses(results []DedupKeyResult) map[int64]string {
	out := map[int64]string{}
	for _, r := range results {
		out[r.StorageID] = r.Status
	}
	return out
}

func namedDedupDest(t *testing.T, database *db.DB, name string) db.StorageDestination {
	t.Helper()
	cfg, _ := json.Marshal(map[string]string{"path": t.TempDir()})
	id, err := database.CreateStorageDestination(db.StorageDestination{Name: name, Type: "local", Config: string(cfg), DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	dest, _ := database.GetStorageDestination(id)
	return dest
}

// TestMiskeyedBackupWarns checks a backup on a server whose vault.key does
// not match the destination still completes through the escrow, and says
// clearly in the run log that the destination is mis-keyed.
func TestMiskeyedBackupWarns(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	_ = database.SetSetting("encryption_passphrase", "pw")
	dest, jobID := dedupBackup(t, runnerWithKey(database, testServerKey()), database, t.TempDir())
	header, _ := os.ReadFile(filepath.Join(storageDirOf(t, dest), "_vault", "repo.json"))

	other := runnerWithKey(database, otherServerKey())
	other.RunJob(jobID)
	// Both runs can start within the same second, so pick the newest by ID
	// rather than trusting the started_at order.
	runs, _ := database.GetJobRuns(jobID, 10)
	var latest db.JobRun
	for _, run := range runs {
		if run.ID > latest.ID {
			latest = run
		}
	}
	if len(runs) < 2 || latest.Status != "completed" {
		t.Fatalf("mis-keyed backup did not complete: %+v", runs)
	}
	entries, err := database.ListRunLogEntries(context.Background(), latest.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, e := range entries {
		warned = warned || strings.Contains(e.Message, "does not match it")
	}
	if !warned {
		t.Fatal("run log has no warning about the mismatched vault.key")
	}
	if after, _ := os.ReadFile(filepath.Join(storageDirOf(t, dest), "_vault", "repo.json")); !bytes.Equal(after, header) {
		t.Fatal("a mis-keyed backup rewrote repo.json")
	}
}

func storageDirOf(t *testing.T, dest db.StorageDestination) string {
	t.Helper()
	var cfg struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(dest.Config), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Path
}

// TestReclaimRemovesEscrowAndHeaderBackups checks deleting the last dedup job
// removes the escrow and repo.json backups — they hold the master key — and
// still leaves the database backups that share _vault/.
func TestReclaimRemovesEscrowAndHeaderBackups(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	dest := namedDedupDest(t, database, "reclaim")
	dir := storageDirOf(t, dest)
	vaultDir := filepath.Join(dir, "_vault")
	for _, name := range []string{"repo.json", "master.escrow.age", "repo.json.20261007T030405Z.bak", "vault.db.latest"} {
		_ = os.MkdirAll(vaultDir, 0o755)
		if err := os.WriteFile(filepath.Join(vaultDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := runnerWithKey(database, testServerKey())
	var errs []error
	r.reclaimDedupAfterJobDelete(adapterFor(t, dest), 1, dest, &errs)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	for _, gone := range []string{"repo.json", "master.escrow.age", "repo.json.20261007T030405Z.bak"} {
		if _, err := os.Stat(filepath.Join(vaultDir, gone)); err == nil {
			t.Errorf("%s survived the repository removal", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "vault.db.latest")); err != nil {
		t.Fatalf("the database backup in _vault/ was removed: %v", err)
	}
}

// failingWriteAdapter refuses every write, to exercise escrow write failures.
type failingWriteAdapter struct{ storage.Adapter }

func (failingWriteAdapter) Write(string, io.Reader) error { return errors.New("storage is read-only") }

// TestDedupKeyMismatchPaths covers the mismatch handling on the paths a
// recovered server hits: the fallback with a wrong passphrase, restore, scan,
// import, a failed escrow write, and an unreachable destination in the
// recovery step.
func TestDedupKeyMismatchPaths(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	_ = database.SetSetting("encryption_passphrase", "pw")
	dest, jobID := dedupBackup(t, runnerWithKey(database, testServerKey()), database, t.TempDir())
	_ = database.SetSetting("encryption_passphrase", "")
	other := runnerWithKey(database, otherServerKey())

	// Wrong passphrase: both causes stay in the chain.
	_, err = OpenDedupRepoWithFallback(database, adapterFor(t, dest), dest.ID, otherServerKey(), "wrong")
	if !errors.Is(err, dedup.ErrServerKeyMismatch) || !errors.Is(err, dedup.ErrPassphraseMismatch) {
		t.Fatalf("wrong passphrase: %v", err)
	}

	// Restore on the mismatched key explains what to do.
	rps, _ := database.ListRestorePoints(jobID)
	err = other.RestoreItem(rps[0], "src", "folder", t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), "different vault.key") {
		t.Fatalf("restore: %v, want the key-mismatch guidance", err)
	}

	// Scan and import cannot decrypt the dedup manifests without a key or
	// passphrase; they report locked entries instead of failing.
	found, err := other.ScanStorageManifests(dest)
	if err != nil {
		t.Fatal(err)
	}
	locked := 0
	for _, m := range found {
		if m["encrypted"] == true {
			locked++
		}
	}
	if locked == 0 {
		t.Fatalf("scan: %+v, want locked placeholders", found)
	}
	if n, err := other.ImportBackups(dest.ID, found); err != nil || n != 0 {
		t.Fatalf("import of locked backups = %d, %v; want 0 imported", n, err)
	}
	// With the passphrase the same scan reads them.
	if found, _ = other.ScanStorageManifests(dest, "pw"); len(found) == 0 || found[0]["encrypted"] == true {
		t.Fatalf("scan with passphrase: %+v", found)
	}

	// A failed escrow write is logged and not cached as confirmed.
	_ = database.SetSetting("encryption_passphrase", "pw")
	repo, err := dedup.OpenRepo(database, failingWriteAdapter{adapterFor(t, dest)}, dest.ID, testServerKey())
	if err != nil {
		t.Fatal(err)
	}
	owner := runnerWithKey(database, testServerKey())
	if _, err := repo.EnsurePassphraseEscrow("changed"); err == nil {
		t.Fatal("write through a failing adapter succeeded")
	}
	_ = database.SetSetting("encryption_passphrase", "changed")
	owner.ensureDedupEscrow(repo, dest)
	owner.escrowMu.Lock()
	cached := owner.escrowed[dest.ID]
	owner.escrowMu.Unlock()
	if cached != "" {
		t.Fatal("a failed escrow write was cached as confirmed")
	}

	// An unreachable destination is reported, not fatal for the rest.
	bad, _ := database.CreateStorageDestination(db.StorageDestination{Name: "unreachable", Type: "sftp", Config: "{", DedupEnabled: true})
	results, err := owner.RewrapDedupKeys("pw")
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses(results); got[bad] != "error" || got[dest.ID] != "ok" {
		t.Fatalf("recovery with an unreachable destination: %+v", results)
	}
}

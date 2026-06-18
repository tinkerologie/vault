package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
)

// stubFreeSpace overrides freeSpaceAt for a test and restores it after.
func stubFreeSpace(t *testing.T, free int64) {
	t.Helper()
	orig := freeSpaceAt
	freeSpaceAt = func(string) (int64, error) { return free, nil }
	t.Cleanup(func() { freeSpaceAt = orig })
}

func checkByID(res PreflightResult, id string) PreflightCheck {
	for _, c := range res.Checks {
		if c.ID == id {
			return c
		}
	}
	return PreflightCheck{}
}

func newLocalDest(t *testing.T, d *db.DB, storageDir string, dedup bool) (db.StorageDestination, db.Job) {
	t.Helper()
	id, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "local", Type: "local", Config: `{"path":"` + storageDir + `"}`, DedupEnabled: dedup,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	return db.StorageDestination{ID: id}, db.Job{ID: 1, StorageDestID: id, Encryption: "none"}
}

func TestPreflightRestore_AllGreen(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close() //nolint:errcheck

	storageDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(storageDir, "job1/run1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "job1/run1/config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, job := newLocalDest(t, d, storageDir, false)
	rp := db.RestorePoint{StoragePath: "job1/run1", SizeBytes: 1000}

	stubFreeSpace(t, 1<<40) // 1 TiB free
	r := &Runner{db: d}
	res := r.PreflightRestore(job, rp, "", "")

	if !res.OK {
		t.Fatalf("expected OK preflight, got %+v", res)
	}
	if s := checkByID(res, "reachable").Status; s != "ok" {
		t.Errorf("reachable = %q, want ok", s)
	}
	if s := checkByID(res, "present").Status; s != "ok" {
		t.Errorf("present = %q, want ok", s)
	}
	if s := checkByID(res, "decryptable").Status; s != "skip" {
		t.Errorf("decryptable = %q, want skip (unencrypted)", s)
	}
	if s := checkByID(res, "space").Status; s != "ok" {
		t.Errorf("space = %q, want ok", s)
	}
}

func TestPreflightRestore_MissingRestorePointFails(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()           //nolint:errcheck
	storageDir := t.TempDir() // no restore-point dir created
	_, job := newLocalDest(t, d, storageDir, false)
	rp := db.RestorePoint{StoragePath: "job1/gone", SizeBytes: 1000}

	stubFreeSpace(t, 1<<40)
	res := (&Runner{db: d}).PreflightRestore(job, rp, "", "")

	if res.OK {
		t.Fatal("expected preflight to fail when restore point is missing on storage")
	}
	if s := checkByID(res, "present").Status; s != "fail" {
		t.Errorf("present = %q, want fail", s)
	}
}

func TestPreflightRestore_EncryptedPassphrase(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close() //nolint:errcheck
	storageDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(storageDir, "j/r"), 0o755)
	_ = os.WriteFile(filepath.Join(storageDir, "j/r/x"), []byte("x"), 0o644)
	_, job := newLocalDest(t, d, storageDir, false)
	job.Encryption = "age"
	hash, _ := crypto.HashPassphrase("correct horse")
	if err := d.SetSetting("encryption_passphrase_hash", hash); err != nil {
		t.Fatal(err)
	}
	rp := db.RestorePoint{StoragePath: "j/r", SizeBytes: 10}
	stubFreeSpace(t, 1<<40)
	r := &Runner{db: d}

	// Missing passphrase -> blocking fail.
	if res := r.PreflightRestore(job, rp, "", ""); res.OK || checkByID(res, "decryptable").Status != "fail" {
		t.Errorf("empty passphrase: want decryptable fail, got %+v", checkByID(res, "decryptable"))
	}
	// Wrong passphrase -> fail.
	if res := r.PreflightRestore(job, rp, "wrong", ""); res.OK || checkByID(res, "decryptable").Status != "fail" {
		t.Errorf("wrong passphrase: want decryptable fail, got %+v", checkByID(res, "decryptable"))
	}
	// Correct passphrase -> ok.
	if res := r.PreflightRestore(job, rp, "correct horse", ""); !res.OK || checkByID(res, "decryptable").Status != "ok" {
		t.Errorf("correct passphrase: want decryptable ok + OK, got %+v", res)
	}
}

func TestPreflightRestore_LowSpaceWarns(t *testing.T) {
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close() //nolint:errcheck
	storageDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(storageDir, "j/r"), 0o755)
	_ = os.WriteFile(filepath.Join(storageDir, "j/r/x"), []byte("x"), 0o644)
	_, job := newLocalDest(t, d, storageDir, false)
	rp := db.RestorePoint{StoragePath: "j/r", SizeBytes: 10_000_000}

	stubFreeSpace(t, 1000) // far less than the backup size
	res := (&Runner{db: d}).PreflightRestore(job, rp, "", "")

	// Low space is a non-blocking warning: still OK overall, but flagged.
	if !res.OK {
		t.Errorf("low space should not block, got OK=false: %+v", res)
	}
	if s := checkByID(res, "space").Status; s != "warn" {
		t.Errorf("space = %q, want warn", s)
	}
}

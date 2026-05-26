package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestWriteDBOnce_OpenFailure drives the os.Open error branch by pointing
// dbPath at a file that doesn't exist.
func TestWriteDBOnce_OpenFailure(t *testing.T) {
	t.Parallel()

	adapter := newRecordingAdapter()
	missing := filepath.Join(t.TempDir(), "no-such.db")

	if err := writeDBOnce(adapter, missing, "_vault/x", ""); err == nil {
		t.Fatal("expected open error for missing db file")
	}
}

// TestWriteDBOnce_AdapterWriteFails drives the adapter.Write error branch:
// the writeDBOnce succeeds at opening + reading, then the adapter returns
// an error. We do not assert details — just that the wrapper bubbles it.
func TestWriteDBOnce_AdapterWriteFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vault.db")
	if err := writeFileSafe(t, dbPath, []byte("payload")); err != nil {
		t.Fatalf("setup: %v", err)
	}

	adapter := newRecordingAdapter()
	adapter.err = errInjected

	err := writeDBOnce(adapter, dbPath, "_vault/x", "")
	if err == nil {
		t.Fatal("expected error from adapter.Write")
	}
}

// errInjected is a sentinel used by the adapter mock to trigger the
// per-call error path.
var errInjected = injectedErr{"injected adapter write error"}

type injectedErr struct{ msg string }

func (e injectedErr) Error() string { return e.msg }

// TestBackupDatabase_HappyPath drives backupDatabase end-to-end with
// a real DB file (not :memory:) and a real local destination. The
// runner's DB is the file-backed one created by newTestRunner.
func TestBackupDatabase_HappyPath(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	storageDir := filepath.Join(t.TempDir(), "store")
	cfg := `{"path":"` + storageDir + `"}`
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "dbb-" + uniqueDBB(),
		Type:   "local",
		Config: cfg,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	jobDest := db.StorageDestination{ID: destID, Type: "local", Config: cfg}
	r.backupDatabase(jobDest)

	// Confirm both timestamped + latest pointer files were written.
	entries, err := os.ReadDir(filepath.Join(storageDir, dbBackupBaseDir))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("expected at least 2 DB backup files (timestamped + latest), got %d", len(entries))
	}
}

// TestBackupDatabaseToDest_BadConfig drives the adapter-creation
// error branch (corrupt JSON config).
func TestBackupDatabaseToDest_BadConfig(t *testing.T) {
	t.Parallel()
	r, _ := newTestRunner(t)
	dest := db.StorageDestination{Name: "bad", Type: "local", Config: `{not valid json`}
	dbPath := filepath.Join(t.TempDir(), "vault.db")
	if err := writeFileSafe(t, dbPath, []byte("dummy")); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Should not panic; just logs and returns.
	r.backupDatabaseToDest(dest, dbPath, "")
}

// Tiny unique-name helper isolated from other tests' counters.
var dbbSeq int64

func uniqueDBB() string {
	dbbSeq++
	return string(rune('a' + (dbbSeq % 26)))
}

package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// newFileBackedStorageHandler returns a StorageHandler whose underlying
// vault.db is a real on-disk SQLite file (NOT in-memory) so RestoreDB's
// "h.db.Path()" check passes and the file-swap branches can actually run.
// destID points at a freshly created local storage destination rooted at a
// temp dir, so callers can write a fake vault.db inside.
func newFileBackedStorageHandler(t *testing.T) (*StorageHandler, int64, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "vault.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	storageDir := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "restore-db-dest",
		Type:   "local",
		Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	serverKey := bytes.Repeat([]byte{0xcd}, 32)
	hub := ws.NewHub()
	go hub.Run()
	r := runner.New(d, hub, serverKey)

	return NewStorageHandler(d, r), destID, storageDir
}

func TestRestoreDB_InvalidID(t *testing.T) {
	t.Parallel()
	h, _, _ := newFileBackedStorageHandler(t)

	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/bad/restore-db",
		"bad", []byte(`{"storage_path":"foo"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestRestoreDB_NotFound(t *testing.T) {
	t.Parallel()
	h, _, _ := newFileBackedStorageHandler(t)

	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/9999/restore-db",
		"9999", []byte(`{"storage_path":"foo"}`)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestRestoreDB_InvalidJSON(t *testing.T) {
	t.Parallel()
	h, destID, _ := newFileBackedStorageHandler(t)
	idStr := strconv.FormatInt(destID, 10)

	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/x/restore-db",
		idStr, []byte("not-json")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestRestoreDB_VaultDBNotFoundOnStorage(t *testing.T) {
	t.Parallel()
	h, destID, _ := newFileBackedStorageHandler(t)
	idStr := strconv.FormatInt(destID, 10)

	// Valid storage path, but no vault.db file exists in the backup dir.
	body := []byte(`{"storage_path":"some-backup"}`)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/x/restore-db",
		idStr, body))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

func TestRestoreDB_DownloadedFileIsNotValidDB(t *testing.T) {
	t.Parallel()
	h, destID, storageDir := newFileBackedStorageHandler(t)
	idStr := strconv.FormatInt(destID, 10)

	// Write a non-SQLite file at the expected location.
	backupDir := filepath.Join(storageDir, "fake-backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "vault.db"),
		[]byte("not-a-sqlite-file"), 0o644); err != nil {
		t.Fatalf("write fake db: %v", err)
	}

	body := []byte(`{"storage_path":"fake-backup"}`)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/x/restore-db",
		idStr, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (invalid DB); body: %s", w.Code, w.Body.String())
	}
}

func TestRestoreDB_SuccessSwapsDB(t *testing.T) {
	t.Parallel()
	h, destID, storageDir := newFileBackedStorageHandler(t)
	idStr := strconv.FormatInt(destID, 10)

	// Produce a valid SQLite database file by opening + closing a new one
	// somewhere outside the live DB path, then copying it into the storage
	// backup directory.
	srcDBPath := filepath.Join(t.TempDir(), "source.db")
	srcDB, err := db.Open(srcDBPath)
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	if err := srcDB.Close(); err != nil {
		t.Fatalf("close source db: %v", err)
	}

	backupDir := filepath.Join(storageDir, "good-backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("mkdir backup: %v", err)
	}
	srcBytes, err := os.ReadFile(srcDBPath)
	if err != nil {
		t.Fatalf("read source db: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "vault.db"), srcBytes, 0o644); err != nil {
		t.Fatalf("copy db into backup dir: %v", err)
	}

	body := []byte(`{"storage_path":"good-backup"}`)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/x/restore-db",
		idStr, body))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// The handler closes h.db on success — verify the current DB on disk
	// is the size of our source DB (rough confirmation of the swap).
	// We avoid re-opening since the test's t.Cleanup will try to close h.db
	// again; just compare file sizes.
	st, err := os.Stat(h.db.Path())
	if err != nil {
		t.Fatalf("stat current db: %v", err)
	}
	if st.Size() != int64(len(srcBytes)) {
		t.Errorf("current db size = %d, want %d", st.Size(), len(srcBytes))
	}
}

// keep io.Copy referenced (used inside RestoreDB) so the test file is
// future-proof if direct imports shift.
var _ = io.Copy

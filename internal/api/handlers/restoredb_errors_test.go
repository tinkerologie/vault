package handlers

import (
	"bytes"
	"encoding/json"
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

// TestRestoreDB_InMemoryDatabaseRejected drives the in-memory DB rejection
// branch. Build a SettingsHandler-like StorageHandler whose underlying DB
// is :memory: so h.db.Path() returns ":memory:" and the handler bails.
func TestRestoreDB_InMemoryDatabaseRejected(t *testing.T) {
	t.Parallel()
	// Open an in-memory DB explicitly.
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	storageDir := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "mem-restore-test", Type: "local", Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	serverKey := bytes.Repeat([]byte{0xee}, 32)
	hub := ws.NewHub()
	go hub.Run()
	r := runner.New(d, hub, serverKey)
	h := NewStorageHandler(d, r)

	// Write a valid db file at the storage path.
	srcDBPath := filepath.Join(t.TempDir(), "src.db")
	srcDB, err := db.Open(srcDBPath)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	if err := srcDB.Close(); err != nil {
		t.Fatalf("close src: %v", err)
	}
	srcBytes, err := os.ReadFile(srcDBPath)
	if err != nil {
		t.Fatalf("read src: %v", err)
	}

	backupDir := filepath.Join(storageDir, "mem-backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("mkdir backup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "vault.db"), srcBytes, 0o644); err != nil {
		t.Fatalf("copy db into backup dir: %v", err)
	}

	body := []byte(`{"storage_path":"mem-backup"}`)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/x/restore-db",
		strconv.FormatInt(destID, 10), body))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (in-memory rejected); body: %s",
			w.Code, w.Body.String())
	}
}

// TestRestoreDB_SuccessNoExistingDB exercises the "backupExists == false"
// branch where the current DB file is missing (we delete it after the
// daemon opens it but before RestoreDB runs). The file-swap proceeds
// without taking a backup.
func TestRestoreDB_SuccessNoExistingDB(t *testing.T) {
	t.Parallel()
	h, destID, storageDir := newFileBackedStorageHandler(t)
	idStr := strconv.FormatInt(destID, 10)

	// Write a real DB to the storage backup dir.
	srcDBPath := filepath.Join(t.TempDir(), "src.db")
	srcDB, err := db.Open(srcDBPath)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	if err := srcDB.Close(); err != nil {
		t.Fatalf("close src: %v", err)
	}
	srcBytes, err := os.ReadFile(srcDBPath)
	if err != nil {
		t.Fatalf("read src: %v", err)
	}
	backupDir := filepath.Join(storageDir, "no-existing")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("mkdir backup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "vault.db"), srcBytes, 0o644); err != nil {
		t.Fatalf("copy db: %v", err)
	}

	// Pre-emptively delete the live DB file so the os.Stat(currentPath)
	// check fails and backupExists stays false.
	currentPath := h.db.Path()
	// Close h.db FIRST so we can safely remove the file. Then re-open it
	// (the handler will close it again internally). We use a separate
	// open here just so the handler can call h.db.Path() etc.
	// Actually the handler's h.db.Path() returns the path string even if
	// the DB is closed. So just removing the file should work, but we
	// also need h.db.Close() to NOT fail. The handler calls h.db.Close()
	// after the validation passes; closing an already-closed sqlite is
	// idempotent (returns nil in our adapter).
	if err := os.Remove(currentPath); err != nil {
		t.Fatalf("remove current db: %v", err)
	}
	// Also remove WAL/SHM if they exist.
	_ = os.Remove(currentPath + "-wal")
	_ = os.Remove(currentPath + "-shm")

	body := []byte(`{"storage_path":"no-existing"}`)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/api/v1/storage/x/restore-db",
		idStr, body))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

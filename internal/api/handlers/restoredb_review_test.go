package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
)

// writePlaintextDBFixture builds a valid vault DB and copies it to
// <storageDir>/<relPath>. Returns relPath.
func writePlaintextDBFixture(t *testing.T, storageDir, relPath string) string {
	t.Helper()
	srcPath := filepath.Join(t.TempDir(), "fixture.db")
	fd, err := db.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := fd.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(storageDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return relPath
}

// A failure after h.db.Close() must restore the file AND reopen the handle —
// otherwise the daemon fails every request until a manual restart.
func TestRestoreDB_SwapFailureReopensDB(t *testing.T) {
	h, destID, storageDir := newFileBackedStorageHandler(t)
	snapPath := writePlaintextDBFixture(t, storageDir, "_vault/vault.db.latest.db")

	// Plant a non-empty directory at the .bak path: os.Remove can't clear it
	// and os.Rename(current, bak) can't replace it, forcing a failure on the
	// post-Close swap path.
	bakPath := h.db.Path() + ".bak"
	if err := os.MkdirAll(filepath.Join(bakPath, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"storage_path": %q}`, snapPath)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/storage/{id}/restore-db",
		strconv.FormatInt(destID, 10), []byte(body)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
	if err := h.db.Ping(); err != nil {
		t.Fatalf("DB handle unusable after failed restore: %v", err)
	}
}

// A backup that opens but fails PRAGMA integrity_check must be rejected
// before the live DB is touched. db.Open may catch some corruption first —
// either failure mode is a 400 with the working DB untouched.
func TestRestoreDB_CorruptBackupRejected(t *testing.T) {
	h, destID, storageDir := newFileBackedStorageHandler(t)
	snapPath := writePlaintextDBFixture(t, storageDir, "_vault/vault.db.latest.db")

	// Corrupt a page past the header: the SQLite magic stays intact, so only
	// open-time validation or the integrity check can catch it.
	full := filepath.Join(storageDir, snapPath)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 8192 {
		t.Fatalf("fixture too small to corrupt a page: %d bytes", len(data))
	}
	for i := 4096; i < 8192; i++ {
		data[i] ^= 0xff
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"storage_path": %q}`, snapPath)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/storage/{id}/restore-db",
		strconv.FormatInt(destID, 10), []byte(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if err := h.db.Ping(); err != nil {
		t.Fatalf("working DB touched by rejected restore: %v", err)
	}
}

// An adapter List error that is NOT "directory missing" must surface as an
// error, not as an empty backup list (which reads as "no backups exist").
func TestListDBBackupsAdapterError(t *testing.T) {
	h, destID, storageDir := newFileBackedStorageHandler(t)
	// A regular FILE named _vault makes os.ReadDir fail with ENOTDIR — a real
	// adapter error, not "no backups yet".
	if err := os.WriteFile(filepath.Join(storageDir, "_vault"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ListDBBackups(w, reqWithID(http.MethodGet, "/storage/{id}/db-backups",
		strconv.FormatInt(destID, 10), nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body = %s", w.Code, w.Body.String())
	}
}

// verify_only on a plaintext file must actually validate the payload.
func TestRestoreDBVerifyOnlyPlaintextGarbage(t *testing.T) {
	h, destID, storageDir := newFileBackedStorageHandler(t)
	vdir := filepath.Join(storageDir, "_vault")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vdir, "vault.db.latest.db"),
		[]byte("definitely not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := `{"storage_path": "_vault/vault.db.latest.db", "verify_only": true}`
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/storage/{id}/restore-db",
		strconv.FormatInt(destID, 10), []byte(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "doesn't look like") {
		t.Fatalf("body = %s, want friendly not-a-backup message", w.Body.String())
	}
	if err := h.db.Ping(); err != nil {
		t.Fatalf("working DB unusable after verify_only: %v", err)
	}
}

func TestRestoreDBVerifyOnlyPlaintextValid(t *testing.T) {
	h, destID, storageDir := newFileBackedStorageHandler(t)
	snapPath := writePlaintextDBFixture(t, storageDir, "_vault/vault.db.latest.db")

	body := fmt.Sprintf(`{"storage_path": %q, "verify_only": true}`, snapPath)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/storage/{id}/restore-db",
		strconv.FormatInt(destID, 10), []byte(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"valid":true`) {
		t.Fatalf("body = %s, want valid:true", w.Body.String())
	}
	if err := h.db.Ping(); err != nil {
		t.Fatalf("working DB unusable after verify_only: %v", err)
	}
}

// Concurrent restores are serialized: a second request gets 409 instead of
// racing the file swap.
func TestRestoreDBConflictWhenRestoreInProgress(t *testing.T) {
	h, destID, _ := newFileBackedStorageHandler(t)
	h.restoreMu.Lock()
	defer h.restoreMu.Unlock()

	body := `{"storage_path": "_vault/vault.db.latest.db"}`
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/storage/{id}/restore-db",
		strconv.FormatInt(destID, 10), []byte(body)))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
}

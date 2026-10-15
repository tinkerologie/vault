package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
)

// writePlainDBFixture writes a valid plaintext vault DB, customised by prep,
// to <storageDir>/<rel>/vault.db and returns rel.
func writePlainDBFixture(t *testing.T, storageDir, rel string, prep func(*db.DB)) string {
	t.Helper()
	srcPath := filepath.Join(t.TempDir(), "fixture.db")
	fd, err := db.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if prep != nil {
		prep(fd)
	}
	if err := fd.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(storageDir, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vault.db"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return rel
}

// TestRestoreDB458_PlaintextStaleSealAndReloadError restores a plaintext DB
// whose sealed passphrase came from another server, with a scheduler reload
// hook that fails. Both are logged, not fatal.
func TestRestoreDB458_PlaintextStaleSealAndReloadError(t *testing.T) {
	t.Parallel()
	h, destID, storageDir := newFileBackedStorageHandler(t)
	h.SetScheduleReloadHook(func() error { return errors.New("reload boom") })
	rel := writePlainDBFixture(t, storageDir, "plain", func(fd *db.DB) {
		if err := fd.SetSetting("encryption_passphrase_sealed", "stale-old-server-seal"); err != nil {
			t.Fatal(err)
		}
	})
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/x", idStr(destID), []byte(`{"storage_path":"`+rel+`"}`)))
	wantStatus458(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"resealed":false`) {
		t.Errorf("body = %s", w.Body.String())
	}
}

// TestRestoreDB458_ResealWriteFails restores an encrypted DB that refuses
// writes to its sealed-passphrase setting, so re-sealing is logged.
func TestRestoreDB458_ResealWriteFails(t *testing.T) {
	t.Parallel()
	h, destID, storageDir := newFileBackedStorageHandler(t)
	const pass = "correct horse battery"
	hash, err := crypto.HashPassphrase(pass)
	if err != nil {
		t.Fatal(err)
	}
	snap := writeEncryptedDBFixture(t, storageDir, pass, func(fd *db.DB) {
		if err := fd.SetSetting("encryption_passphrase_hash", hash); err != nil {
			t.Fatal(err)
		}
		for _, op := range []string{"INSERT", "UPDATE"} {
			stmt := fmt.Sprintf(`CREATE TRIGGER block_seal_%s BEFORE %s ON settings
				WHEN NEW.key = 'encryption_passphrase_sealed'
				BEGIN SELECT RAISE(ABORT, 'blocked'); END`, strings.ToLower(op), op)
			if _, err := fd.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	})
	body := fmt.Sprintf(`{"storage_path": %q, "passphrase": %q}`, snap, pass)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/x", idStr(destID), []byte(body)))
	wantStatus458(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"resealed":false`) {
		t.Errorf("body = %s", w.Body.String())
	}
}

// TestRestoreDB458_ReadTransportError: a non-not-found read failure maps to
// 502 rather than "no backup found".
func TestRestoreDB458_ReadTransportError(t *testing.T) {
	t.Parallel()
	h, destID, storageDir := newFileBackedStorageHandler(t)
	if err := os.WriteFile(filepath.Join(storageDir, "afile"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/x", idStr(destID), []byte(`{"storage_path":"afile/sub"}`)))
	if w.Code != http.StatusBadGateway && w.Code != http.StatusNotFound {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
}

// TestRestoreDB458_SourceIsDirectory: reading a directory streams an error
// while copying into the temp file.
func TestRestoreDB458_SourceIsDirectory(t *testing.T) {
	t.Parallel()
	h, destID, storageDir := newFileBackedStorageHandler(t)
	if err := os.MkdirAll(filepath.Join(storageDir, "snap.db"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/x", idStr(destID), []byte(`{"storage_path":"snap.db"}`)))
	if w.Code < 400 {
		t.Fatalf("status = %d, want an error; body: %s", w.Code, w.Body.String())
	}
	// The live DB must still be usable.
	if _, err := h.db.GetSetting("anything", ""); err != nil {
		t.Fatalf("db unusable after failed restore: %v", err)
	}
}

// TestRestoreDB458_CorruptAgeHeader: garbage .age content fails decryption.
func TestRestoreDB458_CorruptAgeHeader(t *testing.T) {
	t.Parallel()
	h, destID, storageDir := newFileBackedStorageHandler(t)
	if err := os.MkdirAll(filepath.Join(storageDir, "_vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "_vault", "x.age"), []byte("definitely not age"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/x", idStr(destID), []byte(`{"storage_path":"_vault/x.age","passphrase":"p"}`)))
	if w.Code != http.StatusBadRequest && w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
}

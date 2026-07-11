package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/crypto"
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

	return NewStorageHandler(d, r, serverKey), destID, storageDir
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

// writeEncryptedDBFixture builds a valid vault DB, optionally customizes it
// via prep, encrypts it with the passphrase, and writes it to
// <storageDir>/_vault/vault.db.latest.age. Returns the storage-relative path.
func writeEncryptedDBFixture(t *testing.T, storageDir, passphrase string, prep func(*db.DB)) string {
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
	f, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc, err := crypto.EncryptReader(passphrase, f)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	if err := os.MkdirAll(filepath.Join(storageDir, "_vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst, err := os.Create(filepath.Join(storageDir, "_vault", "vault.db.latest.age"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, enc); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	return "_vault/vault.db.latest.age"
}

func TestRestoreDBEncrypted(t *testing.T) {
	h, destID, storageDir := newFileBackedStorageHandler(t)
	snapPath := writeEncryptedDBFixture(t, storageDir, "correct horse battery", func(fd *db.DB) {
		if err := fd.SetSetting("restore_marker", "from-backup"); err != nil {
			t.Fatal(err)
		}
	})

	body := fmt.Sprintf(`{"storage_path": %q, "passphrase": "correct horse battery"}`, snapPath)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/storage/{id}/restore-db", strconv.FormatInt(destID, 10), []byte(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	// The handler closed h.db before swapping; open the swapped file directly.
	restored, err := db.Open(h.db.Path())
	if err != nil {
		t.Fatalf("opening restored DB: %v", err)
	}
	defer restored.Close()
	got, _ := restored.GetSetting("restore_marker", "")
	if got != "from-backup" {
		t.Fatalf("restore_marker = %q, want from-backup", got)
	}
}

func TestRestoreDBReopensAndReseals(t *testing.T) {
	h, destID, storageDir := newFileBackedStorageHandler(t)
	reloads := 0
	h.SetScheduleReloadHook(func() error { reloads++; return nil })
	configFlushes := 0
	h.SetConfigChangeHook(func() { configFlushes++ })

	const pass = "correct horse battery"
	hash, err := crypto.HashPassphrase(pass)
	if err != nil {
		t.Fatal(err)
	}
	snapPath := writeEncryptedDBFixture(t, storageDir, pass, func(fd *db.DB) {
		// The backup contains the hash plus a seal made with the OLD
		// server's key — unusable on this "new" server.
		if err := fd.SetSetting("encryption_passphrase_hash", hash); err != nil {
			t.Fatal(err)
		}
		if err := fd.SetSetting("encryption_passphrase_sealed", "stale-old-server-seal"); err != nil {
			t.Fatal(err)
		}
		if err := fd.SetSetting("restore_marker", "from-backup"); err != nil {
			t.Fatal(err)
		}
	})

	body := fmt.Sprintf(`{"storage_path": %q, "passphrase": %q}`, snapPath, pass)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/storage/{id}/restore-db", strconv.FormatInt(destID, 10), []byte(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"resealed":true`) {
		t.Fatalf("body = %s, want resealed:true", w.Body.String())
	}

	// Reopen happened in-process: the SAME handle sees restored data.
	got, err := h.db.GetSetting("restore_marker", "")
	if err != nil || got != "from-backup" {
		t.Fatalf("marker via original handle = %q (err %v), want from-backup", got, err)
	}

	// Re-seal used the current server key (0xcd repeated — see helper).
	sealed, _ := h.db.GetSetting("encryption_passphrase_sealed", "")
	serverKey := bytes.Repeat([]byte{0xcd}, 32)
	unsealed, err := crypto.Unseal(serverKey, sealed)
	if err != nil || unsealed != pass {
		t.Fatalf("unsealed = %q (err %v), want %q", unsealed, err, pass)
	}

	if reloads != 1 {
		t.Fatalf("scheduler reloads = %d, want 1", reloads)
	}
	// The restored DB must be flushed to flash right away — a power loss
	// before the next periodic flush would otherwise revert the restore.
	if configFlushes != 1 {
		t.Fatalf("config-change hook calls = %d, want 1", configFlushes)
	}
}

func TestRestoreDBWrongPassphrase(t *testing.T) {
	h, destID, storageDir := newFileBackedStorageHandler(t)
	snapPath := writeEncryptedDBFixture(t, storageDir, "right-passphrase", nil)

	body := fmt.Sprintf(`{"storage_path": %q, "passphrase": "wrong-passphrase"}`, snapPath)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/storage/{id}/restore-db", strconv.FormatInt(destID, 10), []byte(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "incorrect passphrase") {
		t.Fatalf("body = %s, want mention of incorrect passphrase", w.Body.String())
	}
	if err := h.db.Ping(); err != nil {
		t.Fatalf("working DB unusable after failed restore: %v", err)
	}
}

func TestRestoreDBEncryptedNeedsPassphrase(t *testing.T) {
	h, destID, storageDir := newFileBackedStorageHandler(t)
	snapPath := writeEncryptedDBFixture(t, storageDir, "any", nil)

	body := fmt.Sprintf(`{"storage_path": %q}`, snapPath)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/storage/{id}/restore-db", strconv.FormatInt(destID, 10), []byte(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if err := h.db.Ping(); err != nil {
		t.Fatalf("working DB unusable after failed restore: %v", err)
	}
}

func TestListDBBackups(t *testing.T) {
	h, destID, storageDir := newFileBackedStorageHandler(t)
	vdir := filepath.Join(storageDir, "_vault")
	if err := os.MkdirAll(filepath.Join(vdir, "somedir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"vault.db.latest.age",
		"vault.db.2026-07-10T04-00-00.age",
		"vault.db.2026-07-11T04-00-00.age",
		"unrelated.txt",
	} {
		if err := os.WriteFile(filepath.Join(vdir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w := httptest.NewRecorder()
	h.ListDBBackups(w, reqWithID(http.MethodGet, "/storage/{id}/db-backups", strconv.FormatInt(destID, 10), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var entries []struct {
		Path      string `json:"path"`
		Name      string `json:"name"`
		Encrypted bool   `json:"encrypted"`
		IsLatest  bool   `json:"is_latest"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}
	if !entries[0].IsLatest || entries[0].Name != "vault.db.latest.age" {
		t.Fatalf("first entry should be latest, got %+v", entries[0])
	}
	if entries[1].Name != "vault.db.2026-07-11T04-00-00.age" {
		t.Fatalf("timestamped order wrong: %+v", entries)
	}
	if !entries[0].Encrypted {
		t.Fatal("encrypted flag missing on .age entry")
	}
}

func TestListDBBackupsEmpty(t *testing.T) {
	h, destID, _ := newFileBackedStorageHandler(t)
	w := httptest.NewRecorder()
	h.ListDBBackups(w, reqWithID(http.MethodGet, "/storage/{id}/db-backups", strconv.FormatInt(destID, 10), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := strings.TrimSpace(w.Body.String()); got != "[]" {
		t.Fatalf("body = %q, want []", got)
	}
}

func TestRestoreDBVerifyOnly(t *testing.T) {
	h, destID, storageDir := newFileBackedStorageHandler(t)
	snapPath := writeEncryptedDBFixture(t, storageDir, "verify-me", nil)

	body := fmt.Sprintf(`{"storage_path": %q, "passphrase": "verify-me", "verify_only": true}`, snapPath)
	w := httptest.NewRecorder()
	h.RestoreDB(w, reqWithID(http.MethodPost, "/storage/{id}/restore-db", strconv.FormatInt(destID, 10), []byte(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"valid":true`) {
		t.Fatalf("body = %s, want valid:true", w.Body.String())
	}
	if err := h.db.Ping(); err != nil {
		t.Fatalf("working DB closed by verify_only: %v", err)
	}
}

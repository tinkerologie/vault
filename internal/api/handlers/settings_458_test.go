package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

// settingsHandler458 returns a SettingsHandler on a file-backed DB plus the DB
// so tests can install failure triggers.
func settingsHandler458(t *testing.T, key []byte) (*SettingsHandler, *db.DB) {
	t.Helper()
	d := newTestDB(t)
	return NewSettingsHandler(d, key), d
}

// failSettingKey458 makes writes of one settings key fail.
func failSettingKey458(t *testing.T, d *db.DB, key string) {
	t.Helper()
	for _, op := range []string{"INSERT", "UPDATE"} {
		name := "fail458_setting_" + strings.ReplaceAll(key, ".", "_") + "_" + strings.ToLower(op)
		stmt := "CREATE TRIGGER " + name + " BEFORE " + op + " ON settings WHEN NEW.key = '" + key +
			"' BEGIN SELECT RAISE(ABORT, 'forced failure 458'); END"
		if _, err := d.Exec(stmt); err != nil {
			t.Fatalf("trigger: %v", err)
		}
	}
}

type snapMgr458 struct {
	mig *db.SnapshotMigration
	err error
}

func (m *snapMgr458) SnapshotPath() string        { return "/x/vault.db" }
func (m *snapMgr458) DefaultSnapshotPath() string { return "/x/vault.db" }
func (m *snapMgr458) SetSnapshotPath(string) (*db.SnapshotMigration, error) {
	return m.mig, m.err
}
func (m *snapMgr458) LastSnapshot() time.Time                { return time.Time{} }
func (m *snapMgr458) RestorationSource() *db.RestorationInfo { return nil }

func TestSettingsSetSnapshotPath458(t *testing.T) {
	t.Parallel()

	t.Run("store error", func(t *testing.T) {
		t.Parallel()
		h, d := settingsHandler458(t, nil)
		failSettingKey458(t, d, "snapshot_path_override")
		w := httptest.NewRecorder()
		h.SetSnapshotPath(w, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"snapshot_path":""}`)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("runtime apply error reported as migration warning", func(t *testing.T) {
		t.Parallel()
		h, _ := settingsHandler458(t, nil)
		h.SetSnapshotManager(&snapMgr458{err: errors.New("disk full")})
		w := httptest.NewRecorder()
		h.SetSnapshotPath(w, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"snapshot_path":""}`)))
		wantStatus458(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), "could not be written to the new location") {
			t.Errorf("body = %s", w.Body.String())
		}
	})

	t.Run("migration reported", func(t *testing.T) {
		t.Parallel()
		h, _ := settingsHandler458(t, nil)
		h.SetSnapshotManager(&snapMgr458{mig: &db.SnapshotMigration{From: "/a", To: "/b"}})
		w := httptest.NewRecorder()
		h.SetSnapshotPath(w, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"snapshot_path":""}`)))
		wantStatus458(t, w, http.StatusOK)
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp["migration"] == nil {
			t.Errorf("missing migration: %v", resp)
		}
	})
}

func TestSettingsUpdate458_ReplicationReloadError(t *testing.T) {
	t.Parallel()
	h, _ := settingsHandler458(t, nil)
	calls := 0
	h.SetScheduleReloadHook(func() error { calls++; return errors.New("reload failed") })
	w := httptest.NewRecorder()
	h.Update(w, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"replication_enabled":"true"}`)))
	wantStatus458(t, w, http.StatusOK)
	if calls != 1 {
		t.Errorf("reload calls = %d, want 1", calls)
	}
}

func TestSettingsSetEncryption458_Failures(t *testing.T) {
	t.Parallel()

	t.Run("seal error with invalid server key", func(t *testing.T) {
		t.Parallel()
		h, _ := settingsHandler458(t, []byte("short"))
		w := httptest.NewRecorder()
		h.SetEncryption(w, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"passphrase":"longenough1"}`)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("sealed store error", func(t *testing.T) {
		t.Parallel()
		h, d := settingsHandler458(t, bytes.Repeat([]byte{1}, 32))
		failSettingKey458(t, d, "encryption_passphrase_sealed")
		w := httptest.NewRecorder()
		h.SetEncryption(w, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"passphrase":"longenough1"}`)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
}

func TestSettingsGetEncryptionPassphrase458_UnsealError(t *testing.T) {
	t.Parallel()
	h, d := settingsHandler458(t, bytes.Repeat([]byte{2}, 32))
	if err := d.SetSetting("encryption_passphrase_sealed", "garbage-seal"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.GetEncryptionPassphrase(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	wantStatus458(t, w, http.StatusInternalServerError)
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestSettingsSetStagingOverride458_StoreError(t *testing.T) {
	t.Parallel()
	h, d := settingsHandler458(t, nil)
	failSettingKey458(t, d, "staging_dir_override")
	w := httptest.NewRecorder()
	h.SetStagingOverride(w, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"override":""}`)))
	wantStatus458(t, w, http.StatusInternalServerError)
}

func TestSettingsSetAppdataPath458(t *testing.T) {
	t.Parallel()

	t.Run("store error", func(t *testing.T) {
		t.Parallel()
		h, d := settingsHandler458(t, nil)
		failSettingKey458(t, d, "appdata_path")
		w := httptest.NewRecorder()
		h.SetAppdataPath(w, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(`{"path":""}`)))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("symlink into /dev rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := os.Stat("/dev"); err != nil {
			t.Skip("/dev not present")
		}
		dir, err := os.MkdirTemp("/tmp", "vault-458-appdata-*")
		if err != nil {
			t.Skipf("cannot create dir under /tmp: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		link := filepath.Join(dir, "link")
		if err := os.Symlink("/dev", link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		h, _ := settingsHandler458(t, nil)
		body, _ := json.Marshal(map[string]string{"path": link})
		w := httptest.NewRecorder()
		h.SetAppdataPath(w, httptest.NewRequest(http.MethodPut, "/x", bytes.NewReader(body)))
		wantStatus458(t, w, http.StatusBadRequest)
		if !strings.Contains(w.Body.String(), "device and virtual filesystem") {
			t.Errorf("body = %s", w.Body.String())
		}
	})
}

func TestSettingsAPIKey458_Failures(t *testing.T) {
	t.Parallel()

	t.Run("generate seal error", func(t *testing.T) {
		t.Parallel()
		h, _ := settingsHandler458(t, []byte("short"))
		w := httptest.NewRecorder()
		h.GenerateAPIKey(w, httptest.NewRequest(http.MethodPost, "/x", nil))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("generate hash store error rolls back", func(t *testing.T) {
		t.Parallel()
		h, d := settingsHandler458(t, bytes.Repeat([]byte{3}, 32))
		failSettingKey458(t, d, "api_key_hash")
		w := httptest.NewRecorder()
		h.GenerateAPIKey(w, httptest.NewRequest(http.MethodPost, "/x", nil))
		wantStatus458(t, w, http.StatusInternalServerError)
		sealed, _ := d.GetSetting("api_key_sealed", "")
		if sealed != "" {
			t.Errorf("sealed key not rolled back: %q", sealed)
		}
	})

	t.Run("reveal unseal error", func(t *testing.T) {
		t.Parallel()
		h, d := settingsHandler458(t, bytes.Repeat([]byte{4}, 32))
		if err := d.SetSetting("api_key_sealed", "garbage"); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.GetAPIKey(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		wantStatus458(t, w, http.StatusInternalServerError)
	})

	t.Run("revoke sealed clear error", func(t *testing.T) {
		t.Parallel()
		h, d := settingsHandler458(t, bytes.Repeat([]byte{5}, 32))
		failSettingKey458(t, d, "api_key_sealed")
		w := httptest.NewRecorder()
		h.RevokeAPIKey(w, httptest.NewRequest(http.MethodDelete, "/x", nil))
		wantStatus458(t, w, http.StatusInternalServerError)
	})
}

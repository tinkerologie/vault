package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

// Issue #543: the newline would forge an extra vault.cfg record.
func TestSetSnapshotPathRejectsNewline(t *testing.T) {
	h := NewSettingsHandler(newTestDB(t), nil)
	dir, err := os.MkdirTemp("/tmp", "vault-snap-*")
	if err != nil {
		t.Skipf("no /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	// The forged parent exists, so only the control-character check can refuse it.
	forged := filepath.Join(dir, "a\nSNAPSHOT_PATH=/etc/passwd")
	if err := os.MkdirAll(filepath.Dir(forged), 0o750); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"snapshot_path": forged})
	w := httptest.NewRecorder()
	h.SetSnapshotPath(w, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(string(body))))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "control characters") {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
}

func TestSetSnapshotPathRejectsSymlinkEscape(t *testing.T) {
	h := NewSettingsHandler(newTestDB(t), nil)
	dir, err := os.MkdirTemp("/tmp", "vault-snap-*")
	if err != nil {
		t.Skipf("no /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"snapshot_path": filepath.Join(link, "vault.db")})
	w := httptest.NewRecorder()
	h.SetSnapshotPath(w, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(string(body))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
}

// Issue #543: path settings only change through their validating endpoints.
func TestGenericSettingsUpdateRejectsPathSettings(t *testing.T) {
	for _, key := range []string{"snapshot_path_override", "staging_dir_override", "appdata_path"} {
		d := newTestDB(t)
		h := NewSettingsHandler(d, nil)
		body, _ := json.Marshal(map[string]string{key: "/etc/passwd"})
		w := httptest.NewRecorder()
		h.Update(w, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(string(body))))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", key, w.Code)
		}
		if got, _ := d.GetSetting(key, ""); got != "" {
			t.Errorf("%s persisted as %q", key, got)
		}
	}
}

// Issue #543: remap must not point a folder item at an arbitrary host path.
func TestRecoveryRemapRejectsSystemFolderSource(t *testing.T) {
	d := newTestDB(t)
	jobID := seedJob(t, d)
	const original = `{"path":"/mnt/user/share"}`
	itemID, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "f", Settings: original})
	if err != nil {
		t.Fatal(err)
	}
	h := NewRecoveryHandler(d, "v")
	link := filepath.Join(t.TempDir(), "share")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/etc", link} {
		resp := doRemap458(t, h, remapBody458(map[string]any{"kind": "job_item", "id": itemID, "job_id": jobID, "new_path": target}))
		if len(resp.Results) != 1 || resp.Results[0].Applied {
			t.Errorf("%s: %+v", target, resp)
		}
	}
	items, err := d.GetJobItems(jobID)
	if err != nil || items[0].Settings != original {
		t.Fatalf("settings changed: %+v, %v", items, err)
	}
	ok := t.TempDir()
	resp := doRemap458(t, h, remapBody458(map[string]any{"kind": "job_item", "id": itemID, "job_id": jobID, "new_path": ok}))
	if len(resp.Results) != 1 || !resp.Results[0].Applied {
		t.Fatalf("legitimate remap refused: %+v", resp)
	}
}

// A staging override must not reach a system tree through a planted symlink.
func TestSetStagingOverrideRejectsSymlinkEscape(t *testing.T) {
	h := NewSettingsHandler(newTestDB(t), nil)
	dir, err := os.MkdirTemp("/tmp", "vault-stage-*")
	if err != nil {
		t.Skipf("no /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"override": link})
	w := httptest.NewRecorder()
	h.SetStagingOverride(w, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(string(body))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
}

func TestSetAppdataPathRejectsSymlinkEscape(t *testing.T) {
	h := NewSettingsHandler(newTestDB(t), nil)
	dir, err := os.MkdirTemp("/tmp", "vault-appdata-*")
	if err != nil {
		t.Skipf("no /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"path": link})
	w := httptest.NewRecorder()
	h.SetAppdataPath(w, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(string(body))))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "symlink") {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
}

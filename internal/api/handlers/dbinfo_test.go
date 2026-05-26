package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// restoreInfoMockSnapshotManager mirrors mockSnapshotManager but lets the
// test inject a non-nil RestorationSource so the corresponding branch in
// GetDatabaseInfo executes.
type restoreInfoMockSnapshotManager struct {
	snapshotPath string
	info         *db.RestorationInfo
}

func (m *restoreInfoMockSnapshotManager) SnapshotPath() string         { return m.snapshotPath }
func (m *restoreInfoMockSnapshotManager) DefaultSnapshotPath() string  { return "/default/vault.db" }
func (m *restoreInfoMockSnapshotManager) SetSnapshotPath(p string) error {
	m.snapshotPath = p
	return nil
}
func (m *restoreInfoMockSnapshotManager) LastSnapshot() time.Time { return time.Now() }
func (m *restoreInfoMockSnapshotManager) RestorationSource() *db.RestorationInfo {
	return m.info
}

// TestGetDatabaseInfo_WithRestorationSource drives the RestorationSource
// branch with "usb_backup" so the degraded flag is set on the response.
func TestGetDatabaseInfo_WithRestorationSource(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	sm := &restoreInfoMockSnapshotManager{
		snapshotPath: "/nonexistent/vault.db",
		info: &db.RestorationInfo{
			Source: "usb_backup",
			Reason: "test-reason",
		},
	}
	h.SetSnapshotManager(sm)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/database", nil)
	w := httptest.NewRecorder()
	h.GetDatabaseInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["restoration_source"] != "usb_backup" {
		t.Errorf("restoration_source = %v, want usb_backup", resp["restoration_source"])
	}
	if resp["degraded"] != true {
		t.Errorf("degraded = %v, want true", resp["degraded"])
	}
}

// TestGetDatabaseInfo_WithFreshRestoration covers the "fresh" branch of
// the degraded flag.
func TestGetDatabaseInfo_WithFreshRestoration(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	sm := &restoreInfoMockSnapshotManager{
		info: &db.RestorationInfo{Source: "fresh", Reason: "fresh-start"},
	}
	h.SetSnapshotManager(sm)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/database", nil)
	w := httptest.NewRecorder()
	h.GetDatabaseInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["degraded"] != true {
		t.Errorf("degraded = %v, want true", resp["degraded"])
	}
}

// TestGetDatabaseInfo_WithNormalRestoration covers the "snapshot" (i.e.
// non-degraded) restoration path.
func TestGetDatabaseInfo_WithNormalRestoration(t *testing.T) {
	t.Parallel()
	h := newTestSettingsHandler(t)
	sm := &restoreInfoMockSnapshotManager{
		info: &db.RestorationInfo{Source: "snapshot", Reason: "ok"},
	}
	h.SetSnapshotManager(sm)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/database", nil)
	w := httptest.NewRecorder()
	h.GetDatabaseInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["degraded"]; ok {
		t.Errorf("degraded should be absent for non-degraded source")
	}
}

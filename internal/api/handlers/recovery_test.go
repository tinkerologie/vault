package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

func TestNewRecoveryHandler(t *testing.T) {
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v1.2.3")
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	if h.version != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", h.version)
	}
}

func TestRecoveryGetPlan_EmptyDB(t *testing.T) {
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v0.0.1")

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/recovery/plan", nil)
	h.GetPlan(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Top-level keys must always be present.
	for _, key := range []string{"server_info", "steps", "warnings", "last_updated"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("response missing key %q", key)
		}
	}

	// server_info must contain vault_version.
	si, ok := resp["server_info"].(map[string]any)
	if !ok {
		t.Fatalf("server_info type %T, want map", resp["server_info"])
	}
	if si["vault_version"] != "v0.0.1" {
		t.Errorf("vault_version = %v, want v0.0.1", si["vault_version"])
	}

	// Empty DB → at least the "Install Vault Plugin" step must be present.
	steps, ok := resp["steps"].([]any)
	if !ok {
		t.Fatalf("steps type %T", resp["steps"])
	}
	if len(steps) < 1 {
		t.Fatal("expected at least 1 step (Install Vault Plugin)")
	}
}

func TestRecoveryGetPlan_WithContainerItems(t *testing.T) {
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v1.0.0")

	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "recovery-dest", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name: "recovery-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	_, err = d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "container", ItemName: "plex",
	})
	if err != nil {
		t.Fatalf("add item: %v", err)
	}

	// Create a run and restore point so the container is "protected".
	runID, err := d.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "running", BackupType: "full",
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	now := time.Now().UTC()
	if err := d.UpdateJobRun(db.JobRun{
		ID:          runID,
		JobID:       jobID,
		Status:      "success",
		BackupType:  "full",
		CompletedAt: &now,
	}); err != nil {
		t.Fatalf("update run: %v", err)
	}
	_, err = d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "/backups/plex", SizeBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("create rp: %v", err)
	}

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/recovery/plan", nil)
	h.GetPlan(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// server_info.total_protected_items must be >= 1.
	si := resp["server_info"].(map[string]any)
	if total, ok := si["total_protected_items"].(float64); !ok || total < 1 {
		t.Errorf("total_protected_items = %v, want >= 1", si["total_protected_items"])
	}

	// There must be a "Restore Containers" step.
	steps := resp["steps"].([]any)
	found := false
	for _, s := range steps {
		sm := s.(map[string]any)
		if title, _ := sm["title"].(string); len(title) > 17 && title[:17] == "Restore Container" {
			found = true
			// Items must contain our container.
			items, ok := sm["items"].([]any)
			if !ok || len(items) == 0 {
				t.Errorf("Restore Containers step has no items")
			}
		}
	}
	if !found {
		t.Errorf("expected a 'Restore Containers' step, got steps: %v", steps)
	}
}

func TestRecoveryGetPlan_WithMixedItems(t *testing.T) {
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v1.0.0")

	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "mixed-dest", Type: "local", Config: `{"path":"/tmp"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name: "mixed-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// Add container + vm + folder items.
	for _, item := range []db.JobItem{
		{JobID: jobID, ItemType: "container", ItemName: "nginx"},
		{JobID: jobID, ItemType: "vm", ItemName: "windows10"},
		{JobID: jobID, ItemType: "folder", ItemName: "/mnt/user/data"},
		{JobID: jobID, ItemType: "folder", ItemName: "/boot", Settings: `{"preset":"flash"}`},
		{JobID: jobID, ItemType: "folder", ItemName: "/mnt/user/broken", Settings: `{not-json`},
	} {
		if _, err := d.AddJobItem(item); err != nil {
			t.Fatalf("add item %s: %v", item.ItemName, err)
		}
	}

	// No restore points → items should show up as unprotected.
	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/recovery/plan", nil)
	h.GetPlan(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Warnings should mention items with no restore points.
	warnings, _ := resp["warnings"].([]any)
	if len(warnings) < 3 {
		t.Errorf("expected >= 3 warnings for unprotected items, got %d", len(warnings))
	}
	hasInvalidSettingsWarning := false
	for _, rawWarning := range warnings {
		if strings.Contains(rawWarning.(string), "invalid folder settings") {
			hasInvalidSettingsWarning = true
		}
	}
	if !hasInvalidSettingsWarning {
		t.Errorf("expected invalid folder settings warning, got %v", warnings)
	}

	// Each item type should generate its own step.
	steps := resp["steps"].([]any)
	hasContainers := false
	hasVMs := false
	hasFolders := false
	hasFlashPreset := false
	for _, s := range steps {
		sm := s.(map[string]any)
		title, _ := sm["title"].(string)
		switch {
		case len(title) >= 18 && title[:18] == "Restore Containers":
			hasContainers = true
		case len(title) >= 24 && title[:24] == "Restore Virtual Machines":
			hasVMs = true
		case len(title) >= 15 && title[:15] == "Restore Folders":
			hasFolders = true
			for _, rawItem := range sm["items"].([]any) {
				item := rawItem.(map[string]any)
				if item["name"] == "/boot" && item["preset"] == "flash" {
					hasFlashPreset = true
				}
			}
		}
	}
	if !hasContainers {
		t.Error("missing Restore Containers step")
	}
	if !hasVMs {
		t.Error("missing Restore Virtual Machines step")
	}
	if !hasFolders {
		t.Error("missing Restore Folders step")
	}
	if !hasFlashPreset {
		t.Error("flash folder item did not preserve its preset discriminator")
	}
}

func TestRecoveryGetPlan_StepStatuses(t *testing.T) {
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v1.0.0")

	destID, _ := d.CreateStorageDestination(db.StorageDestination{
		Name: "status-dest", Type: "local", Config: `{"path":"/tmp"}`,
	})
	jobID, _ := d.CreateJob(db.Job{
		Name: "status-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	_, _ = d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "container", ItemName: "unprotected-container",
	})

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/recovery/plan", nil)
	h.GetPlan(w, r)

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	steps := resp["steps"].([]any)
	for _, s := range steps {
		sm := s.(map[string]any)
		title, _ := sm["title"].(string)
		if len(title) > 17 && title[:17] == "Restore Container" {
			status, _ := sm["status"].(string)
			if status != "warning" {
				t.Errorf("Restore Containers step: status = %q, want 'warning'", status)
			}
		}
	}
}

func TestRecoveryGetPlan_InstallStepAlwaysReady(t *testing.T) {
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v1.0.0")

	w := httptest.NewRecorder()
	r := newReq(http.MethodGet, "/api/v1/recovery/plan", nil)
	h.GetPlan(w, r)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp) //nolint:errcheck

	steps := resp["steps"].([]any)
	first := steps[0].(map[string]any)
	if first["status"] != "ready" {
		t.Errorf("first step status = %q, want 'ready'", first["status"])
	}
	if first["title"] != "Install Vault Plugin" {
		t.Errorf("first step title = %q, want 'Install Vault Plugin'", first["title"])
	}
}

// min is a local helper for Go < 1.21 builds.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestPathAudit(t *testing.T) {
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v1.0.0")

	goodDir := t.TempDir()
	goodID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "good-local", Type: "local", Config: `{"path":"` + goodDir + `"}`,
	})
	if err != nil {
		t.Fatalf("create good dest: %v", err)
	}
	badID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "bad-local", Type: "local", Config: `{"path":"/mnt/gone/away"}`,
	})
	if err != nil {
		t.Fatalf("create bad dest: %v", err)
	}
	// Non-local destinations must not appear in the audit.
	_, err = d.CreateStorageDestination(db.StorageDestination{
		Name: "remote-sftp", Type: "sftp", Config: `{"path":"/remote"}`,
	})
	if err != nil {
		t.Fatalf("create sftp dest: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name: "audit-job", Enabled: true, StorageDestID: goodID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	itemID, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "media", ItemID: "media",
		Settings: `{"path":"/mnt/gone/media"}`,
	})
	if err != nil {
		t.Fatalf("add item: %v", err)
	}

	w := httptest.NewRecorder()
	h.PathAudit(w, newReq(http.MethodGet, "/api/v1/recovery/path-audit", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Entries []struct {
			Kind   string `json:"kind"`
			ID     int64  `json:"id"`
			JobID  int64  `json:"job_id"`
			Name   string `json:"name"`
			Path   string `json:"path"`
			Exists bool   `json:"exists"`
		} `json:"entries"`
		Candidates []string `json:"candidates"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Entries) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(resp.Entries), resp.Entries)
	}
	byKey := map[string]bool{}
	for _, e := range resp.Entries {
		byKey[e.Kind+":"+e.Path] = e.Exists
		switch {
		case e.Kind == "storage" && e.ID == goodID && e.Path != goodDir:
			t.Errorf("good storage path = %q, want %q", e.Path, goodDir)
		case e.Kind == "job_item" && e.ID != itemID:
			t.Errorf("job_item id = %d, want %d", e.ID, itemID)
		case e.Kind == "job_item" && e.JobID != jobID:
			t.Errorf("job_item job_id = %d, want %d", e.JobID, jobID)
		}
	}
	if !byKey["storage:"+goodDir] {
		t.Errorf("existing storage path flagged exists=false")
	}
	if byKey["storage:/mnt/gone/away"] {
		t.Errorf("missing storage path flagged exists=true (id %d)", badID)
	}
	if byKey["job_item:/mnt/gone/media"] {
		t.Errorf("missing folder item path flagged exists=true")
	}
}

func TestPathRemap(t *testing.T) {
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v1.0.0")

	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "remap-dest", Type: "local", Config: `{"path":"/mnt/gone/away"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	badDestID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "remap-dest-2", Type: "local", Config: `{"path":"/mnt/gone/too"}`,
	})
	if err != nil {
		t.Fatalf("create dest 2: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "remap-job", Enabled: true, StorageDestID: destID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	itemID, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "media", ItemID: "media",
		Settings: `{"path":"/mnt/gone/media","exclude":"*.tmp"}`,
	})
	if err != nil {
		t.Fatalf("add item: %v", err)
	}

	newDir := t.TempDir()
	body := []byte(`{"updates":[
		{"kind":"storage","id":` + itoa(destID) + `,"new_path":"` + newDir + `"},
		{"kind":"job_item","id":` + itoa(itemID) + `,"job_id":` + itoa(jobID) + `,"new_path":"` + newDir + `"},
		{"kind":"storage","id":` + itoa(badDestID) + `,"new_path":"/still/does/not/exist"}
	]}`)

	w := httptest.NewRecorder()
	h.PathRemap(w, newReq(http.MethodPost, "/api/v1/recovery/path-remap", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Results []struct {
			Kind    string `json:"kind"`
			ID      int64  `json:"id"`
			Applied bool   `json:"applied"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results = %d, want 3", len(resp.Results))
	}
	if !resp.Results[0].Applied || resp.Results[0].Error != "" {
		t.Errorf("storage remap: %+v", resp.Results[0])
	}
	if !resp.Results[1].Applied || resp.Results[1].Error != "" {
		t.Errorf("job_item remap: %+v", resp.Results[1])
	}
	if resp.Results[2].Applied || resp.Results[2].Error == "" {
		t.Errorf("bad path should be rejected per-row: %+v", resp.Results[2])
	}

	// The changes must actually be persisted.
	dest, err := d.GetStorageDestination(destID)
	if err != nil {
		t.Fatalf("get dest: %v", err)
	}
	var cfg struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(dest.Config), &cfg); err != nil || cfg.Path != newDir {
		t.Errorf("dest config = %s, want path %s", dest.Config, newDir)
	}
	items, err := d.GetJobItems(jobID)
	if err != nil || len(items) != 1 {
		t.Fatalf("get items: %v (%d)", err, len(items))
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(items[0].Settings), &settings); err != nil {
		t.Fatalf("settings not JSON: %s", items[0].Settings)
	}
	if settings["path"] != newDir {
		t.Errorf("item path = %v, want %s", settings["path"], newDir)
	}
	if settings["exclude"] != "*.tmp" {
		t.Errorf("other settings clobbered: %s", items[0].Settings)
	}

	// A bad payload gets a 400, and an unknown kind a per-row error.
	w = httptest.NewRecorder()
	h.PathRemap(w, newReq(http.MethodPost, "/api/v1/recovery/path-remap", []byte(`{not json`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad JSON status = %d, want 400", w.Code)
	}
	w = httptest.NewRecorder()
	h.PathRemap(w, newReq(http.MethodPost, "/api/v1/recovery/path-remap",
		[]byte(`{"updates":[{"kind":"nope","id":1,"new_path":"`+newDir+`"}]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("unknown kind status = %d", w.Code)
	}
	resp.Results = nil
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Applied || resp.Results[0].Error == "" {
		t.Errorf("unknown kind: %+v", resp.Results)
	}
}

func TestPathRemapGuards(t *testing.T) {
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v1.0.0")

	sftpID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "guard-sftp", Type: "sftp", Config: `{"path":"/remote"}`,
	})
	if err != nil {
		t.Fatalf("create sftp dest: %v", err)
	}
	localID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "guard-local", Type: "local", Config: `{"path":"/mnt/gone"}`,
	})
	if err != nil {
		t.Fatalf("create local dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{
		Name: "guard-job", Enabled: true, StorageDestID: localID,
		BackupTypeChain: "full", Schedule: "@daily",
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	containerItemID, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "container", ItemName: "plex", ItemID: "plex",
	})
	if err != nil {
		t.Fatalf("add container item: %v", err)
	}
	corruptItemID, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "folder", ItemName: "corrupt", ItemID: "corrupt",
		Settings: `{not json`,
	})
	if err != nil {
		t.Fatalf("add corrupt item: %v", err)
	}

	newDir := t.TempDir()
	body := []byte(`{"updates":[
		{"kind":"storage","id":` + itoa(sftpID) + `,"new_path":"` + newDir + `"},
		{"kind":"job_item","id":` + itoa(containerItemID) + `,"job_id":` + itoa(jobID) + `,"new_path":"` + newDir + `"},
		{"kind":"job_item","id":` + itoa(corruptItemID) + `,"job_id":` + itoa(jobID) + `,"new_path":"` + newDir + `"}
	]}`)

	w := httptest.NewRecorder()
	h.PathRemap(w, newReq(http.MethodPost, "/api/v1/recovery/path-remap", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Results []struct {
			Kind    string `json:"kind"`
			ID      int64  `json:"id"`
			Applied bool   `json:"applied"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results = %d, want 3: %+v", len(resp.Results), resp.Results)
	}
	if resp.Results[0].Applied || resp.Results[0].Error != "only local destinations can be remapped" {
		t.Errorf("non-local storage remap: %+v", resp.Results[0])
	}
	if resp.Results[1].Applied || resp.Results[1].Error != "only folder items can be remapped" {
		t.Errorf("non-folder item remap: %+v", resp.Results[1])
	}
	if resp.Results[2].Applied || resp.Results[2].Error != "could not read item settings" {
		t.Errorf("corrupt-settings item remap: %+v", resp.Results[2])
	}

	// Nothing was mutated.
	dest, err := d.GetStorageDestination(sftpID)
	if err != nil {
		t.Fatalf("get sftp dest: %v", err)
	}
	if dest.Config != `{"path":"/remote"}` {
		t.Errorf("sftp config mutated: %s", dest.Config)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A successful remap must fire the config-change hook (DB → flash flush);
// a remap where nothing applied must not.
func TestPathRemapFiresConfigChangeHook(t *testing.T) {
	d := newTestDB(t)
	h := NewRecoveryHandler(d, "v1.0.0")
	calls := 0
	h.SetConfigChangeHook(func() { calls++ })

	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "hook-dest", Type: "local", Config: `{"path":"/mnt/gone/away"}`,
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}

	newDir := t.TempDir()
	body := []byte(`{"updates":[{"kind":"storage","id":` + itoa(destID) + `,"new_path":"` + newDir + `"}]}`)
	w := httptest.NewRecorder()
	h.PathRemap(w, newReq(http.MethodPost, "/api/v1/recovery/path-remap", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	if calls != 1 {
		t.Fatalf("hook calls = %d, want 1", calls)
	}

	// Nothing applied → no hook call.
	body = []byte(`{"updates":[{"kind":"storage","id":` + itoa(destID) + `,"new_path":"/still/does/not/exist"}]}`)
	w = httptest.NewRecorder()
	h.PathRemap(w, newReq(http.MethodPost, "/api/v1/recovery/path-remap", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	if calls != 1 {
		t.Fatalf("hook calls after failed remap = %d, want still 1", calls)
	}
}

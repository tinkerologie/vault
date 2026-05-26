package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	// Each item type should generate its own step.
	steps := resp["steps"].([]any)
	hasContainers := false
	hasVMs := false
	hasFolders := false
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

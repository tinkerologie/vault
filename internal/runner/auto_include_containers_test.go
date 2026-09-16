package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/engine"
)

func createTestLocalDest(t *testing.T, d *db.DB) int64 {
	t.Helper()
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	cfg, _ := json.Marshal(map[string]string{"path": storeDir})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "dest-" + nextUniqueRunner(t),
		Type:   "local",
		Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("create storage dest: %v", err)
	}
	return destID
}

func TestAutoIncludeContainers_AddsNewContainersWithAppdataOnly(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)
	destID := createTestLocalDest(t, d)

	jobID, err := d.CreateJob(db.Job{
		Name:                  "job-auto-include-" + nextUniqueRunner(t),
		StorageDestID:         destID,
		BackupTypeChain:       "full",
		Enabled:               true,
		AutoIncludeContainers: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// Add existing container
	existingSettings, _ := json.Marshal(map[string]any{"id": "c1", "image": "nginx:alpine"})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "container",
		ItemName: "nginx",
		ItemID:   "c1",
		Settings: string(existingSettings),
	}); err != nil {
		t.Fatalf("add existing item: %v", err)
	}

	// Mock live containers: existing nginx + new postgres
	r.containerLister = func() ([]engine.BackupItem, error) {
		return []engine.BackupItem{
			{Name: "nginx", Type: "container", Settings: map[string]any{"id": "c1", "image": "nginx:alpine", "state": "running"}},
			{Name: "postgres", Type: "container", Settings: map[string]any{"id": "c2", "image": "postgres:16", "state": "running"}},
		}, nil
	}

	job, err := d.GetJob(jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	items, err := d.GetJobItems(jobID)
	if err != nil {
		t.Fatalf("get items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 initial item, got %d", len(items))
	}

	r.reconcileAutoIncludeContainers(job, &items)

	if len(items) != 2 {
		t.Fatalf("expected 2 items after reconciliation, got %d", len(items))
	}

	dbItems, err := d.GetJobItems(jobID)
	if err != nil {
		t.Fatalf("get db items: %v", err)
	}
	if len(dbItems) != 2 {
		t.Fatalf("expected 2 db items, got %d", len(dbItems))
	}

	var postgresItem *db.JobItem
	for i := range dbItems {
		if dbItems[i].ItemName == "postgres" {
			postgresItem = &dbItems[i]
			break
		}
	}
	if postgresItem == nil {
		t.Fatalf("expected postgres to be added to job_items")
	}
	if postgresItem.ItemType != "container" {
		t.Errorf("expected ItemType container, got %s", postgresItem.ItemType)
	}
	if postgresItem.ItemID != "c2" {
		t.Errorf("expected ItemID c2, got %s", postgresItem.ItemID)
	}

	parsed, err := postgresItem.ParsedSettings()
	if err != nil {
		t.Fatalf("parse postgres settings: %v", err)
	}
	appdataOnly, ok := parsed["appdata_only"].(bool)
	if !ok || !appdataOnly {
		t.Errorf("expected appdata_only=true in auto-added container settings, got %#v", parsed["appdata_only"])
	}
}

func TestAutoIncludeContainers_SkipsVaultExcludeLabel(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)
	destID := createTestLocalDest(t, d)

	jobID, err := d.CreateJob(db.Job{
		Name:                  "job-skip-label-" + nextUniqueRunner(t),
		StorageDestID:         destID,
		BackupTypeChain:       "full",
		Enabled:               true,
		AutoIncludeContainers: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	r.containerLister = func() ([]engine.BackupItem, error) {
		return []engine.BackupItem{
			{
				Name: "excluded-app",
				Type: "container",
				Settings: map[string]any{
					"id":     "c-ex",
					"image":  "redis:alpine",
					"labels": map[string]string{engine.VaultExcludeLabel: "true"},
				},
			},
			{
				Name: "included-app",
				Type: "container",
				Settings: map[string]any{
					"id":     "c-in",
					"image":  "memcached:alpine",
					"labels": map[string]string{"app": "cache"},
				},
			},
		}, nil
	}

	job, _ := d.GetJob(jobID)
	var items []db.JobItem
	r.reconcileAutoIncludeContainers(job, &items)

	dbItems, err := d.GetJobItems(jobID)
	if err != nil {
		t.Fatalf("get items: %v", err)
	}
	if len(dbItems) != 1 {
		t.Fatalf("expected exactly 1 item added, got %d", len(dbItems))
	}
	if dbItems[0].ItemName != "included-app" {
		t.Errorf("expected included-app, got %s", dbItems[0].ItemName)
	}
}

func TestAutoIncludeContainers_DisabledDoesNothing(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)
	destID := createTestLocalDest(t, d)

	jobID, err := d.CreateJob(db.Job{
		Name:                  "job-disabled-" + nextUniqueRunner(t),
		StorageDestID:         destID,
		BackupTypeChain:       "full",
		Enabled:               true,
		AutoIncludeContainers: false, // Disabled
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	r.containerLister = func() ([]engine.BackupItem, error) {
		return []engine.BackupItem{
			{Name: "untracked", Type: "container", Settings: map[string]any{"id": "c1", "image": "redis:alpine"}},
		}, nil
	}

	// Execute RunJob
	r.RunJob(jobID)

	dbItems, err := d.GetJobItems(jobID)
	if err != nil {
		t.Fatalf("get items: %v", err)
	}
	if len(dbItems) != 0 {
		t.Fatalf("expected 0 items when auto-include is disabled, got %d", len(dbItems))
	}
}

func TestAutoIncludeContainers_RemovesDeletedContainers(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)
	destID := createTestLocalDest(t, d)

	jobID, err := d.CreateJob(db.Job{
		Name:                  "job-prune-" + nextUniqueRunner(t),
		StorageDestID:         destID,
		BackupTypeChain:       "full",
		Enabled:               true,
		Compression:           "none",
		Encryption:            "none",
		AutoIncludeContainers: true,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	// Add an existing item for a container that will no longer exist
	ghostSettings, _ := json.Marshal(map[string]any{"id": "ghost-id"})
	ghostItemID, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "container",
		ItemName: "ghost-container",
		ItemID:   "ghost-id",
		Settings: string(ghostSettings),
	})
	if err != nil {
		t.Fatalf("add ghost item: %v", err)
	}

	// Add a folder item so the job has at least one valid workload to complete
	validFolder := filepath.Join(t.TempDir(), "valid-folder")
	_ = os.MkdirAll(validFolder, 0o755)
	_ = os.WriteFile(filepath.Join(validFolder, "data.txt"), []byte("hello"), 0o644)
	folderSettings, _ := json.Marshal(map[string]any{"path": validFolder})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "folder",
		ItemName: "valid-folder",
		Settings: string(folderSettings),
	}); err != nil {
		t.Fatalf("add folder item: %v", err)
	}

	// Live containers do not include ghost-container
	r.containerLister = func() ([]engine.BackupItem, error) {
		return []engine.BackupItem{}, nil
	}

	// First run: ghost-container is missing for the first time; grace period protects it from immediate pruning
	r.RunJob(jobID)

	dbItems, err := d.GetJobItems(jobID)
	if err != nil {
		t.Fatalf("get items: %v", err)
	}
	var ghostItem *db.JobItem
	for i := range dbItems {
		if dbItems[i].ID == ghostItemID {
			ghostItem = &dbItems[i]
			break
		}
	}
	if ghostItem == nil {
		t.Fatalf("ghost-container should not be pruned on first missing run (within grace period)")
	}
	if ghostItem.MissingSince == nil {
		t.Fatalf("expected ghost-container to have MissingSince set on first missing run")
	}

	// Second run: grace period has elapsed -> ghost-container must be pruned
	r.autoPruneGracePeriod = 1 * time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	r.RunJob(jobID)

	dbItemsAfterPrune, err := d.GetJobItems(jobID)
	if err != nil {
		t.Fatalf("get items after prune: %v", err)
	}
	for _, it := range dbItemsAfterPrune {
		if it.ID == ghostItemID || it.ItemName == "ghost-container" {
			t.Fatalf("expected ghost-container (id %d) to be pruned after grace period expired", ghostItemID)
		}
	}
	if len(dbItemsAfterPrune) != 1 || dbItemsAfterPrune[0].ItemName != "valid-folder" {
		t.Errorf("expected only valid-folder to remain in job_items, got %v", dbItemsAfterPrune)
	}
}

func TestAutoIncludeContainers_DisabledPreservesMissingItems(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)
	destID := createTestLocalDest(t, d)

	jobID, err := d.CreateJob(db.Job{
		Name:                  "job-preserve-" + nextUniqueRunner(t),
		StorageDestID:         destID,
		BackupTypeChain:       "full",
		Enabled:               true,
		Compression:           "none",
		Encryption:            "none",
		AutoIncludeContainers: false, // Disabled
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	ghostSettings, _ := json.Marshal(map[string]any{"id": "ghost-id"})
	ghostItemID, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "container",
		ItemName: "ghost-container",
		ItemID:   "ghost-id",
		Settings: string(ghostSettings),
	})
	if err != nil {
		t.Fatalf("add ghost item: %v", err)
	}

	validFolder := filepath.Join(t.TempDir(), "valid-folder")
	_ = os.MkdirAll(validFolder, 0o755)
	_ = os.WriteFile(filepath.Join(validFolder, "data.txt"), []byte("hello"), 0o644)
	folderSettings, _ := json.Marshal(map[string]any{"path": validFolder})
	if _, err := d.AddJobItem(db.JobItem{
		JobID:    jobID,
		ItemType: "folder",
		ItemName: "valid-folder",
		Settings: string(folderSettings),
	}); err != nil {
		t.Fatalf("add folder item: %v", err)
	}

	r.containerLister = func() ([]engine.BackupItem, error) {
		return []engine.BackupItem{}, nil
	}

	r.RunJob(jobID)

	// When AutoIncludeContainers is false, missing container must NOT be auto-removed
	dbItems, err := d.GetJobItems(jobID)
	if err != nil {
		t.Fatalf("get items: %v", err)
	}
	var foundGhost bool
	for _, it := range dbItems {
		if it.ID == ghostItemID {
			foundGhost = true
			if it.MissingSince == nil {
				t.Errorf("expected MissingSince to be set on ghost item when auto-include is false")
			}
			break
		}
	}
	if !foundGhost {
		t.Fatalf("ghost-container should be preserved in job_items when auto-include is false")
	}
}

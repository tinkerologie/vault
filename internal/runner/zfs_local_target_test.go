package runner

import (
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
)

// A ZFS item created on this server with no settings is backed up from the
// dataset named by item.Name (ZFSHandler.Backup), so a restore must target it.
// Only imported and replicated items, whose names come from a backup or a peer,
// need a dataset configured explicitly.
func TestZFSDatasetFromTrustedItemName(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	dest := createNamedLocalDest(t, database, "zfs-local", root)
	local, err := database.CreateJob(db.Job{Name: "local", BackupTypeChain: "full", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	replicated, err := database.CreateReplicatedJob(db.Job{Name: "[S] remote", StorageDestID: dest.ID, SourceID: 4})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []db.JobItem{
		{JobID: local, ItemType: "zfs", ItemName: "tank/data", Settings: `{}`},
		{JobID: local, ItemType: "zfs", ItemName: "tank/blank", Settings: ``},
		{JobID: replicated, ItemType: "zfs", ItemName: "tank/peer", Settings: `{}`},
	} {
		if _, err := database.AddJobItem(it); err != nil {
			t.Fatal(err)
		}
	}
	// An import marks its ZFS items, so their names are not trusted either.
	imported := buildImportJobItems(map[string]any{"items": []any{
		map[string]any{"name": "tank/victim", "type": "zfs", "settings": map[string]any{"dataset": "tank/victim"}},
		map[string]any{"name": "tank/bare", "type": "zfs"},
	}})
	importedJob, err := database.CreateJob(db.Job{Name: "imported", BackupTypeChain: "full", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range imported {
		it.JobID = importedJob
		if _, err := database.AddJobItem(it); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		job        int64
		item, want string
	}{
		{local, "tank/data", "tank/data"},
		{local, "tank/blank", "tank/blank"},
		{replicated, "tank/peer", ""},
		{importedJob, "tank/victim", ""},
		{importedJob, "tank/bare", ""},
	} {
		if got, err := r.configuredZFSDataset(tt.job, tt.item); err != nil || got != tt.want {
			t.Errorf("configuredZFSDataset(%d, %s) = %q, %v, want %q", tt.job, tt.item, got, err, tt.want)
		}
	}
	for _, it := range imported {
		if !strings.Contains(it.Settings, `"imported":true`) {
			t.Errorf("imported ZFS item not marked: %s", it.Settings)
		}
	}
}

// Items an older importer created carry no marker, but every importer has
// recorded its backups as runs with status "imported". A job whose first run is
// one came from an import, so its item names are not trusted.
func TestZFSDatasetRefusesLegacyImportedItems(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	dest := createNamedLocalDest(t, database, "zfs-legacy", root)
	legacy, err := database.CreateJob(db.Job{Name: "legacy", BackupTypeChain: "full", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddJobItem(db.JobItem{JobID: legacy, ItemType: "zfs", ItemName: "tank/victim", Settings: `{}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateImportedJobRun(db.JobRun{JobID: legacy, Status: "imported", BackupType: "full"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A local job that later imported backups keeps trusting its own items.
	local, err := database.CreateJob(db.Job{Name: "local", BackupTypeChain: "full", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddJobItem(db.JobItem{JobID: local, ItemType: "zfs", ItemName: "tank/data", Settings: `{}`}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"completed", "imported"} {
		if _, err := database.CreateJobRun(db.JobRun{JobID: local, Status: status, BackupType: "full"}); err != nil {
			t.Fatal(err)
		}
	}

	if got, err := r.configuredZFSDataset(legacy, "tank/victim"); err != nil || got != "" {
		t.Fatalf("legacy imported item = %q, %v, want no trusted target", got, err)
	}
	if got, err := r.configuredZFSDataset(local, "tank/data"); err != nil || got != "tank/data" {
		t.Fatalf("local item = %q, %v", got, err)
	}
	// The override: a dataset set on the item is used as is.
	if _, err := database.Exec(`UPDATE job_items SET settings = '{"dataset":"tank/restored"}' WHERE job_id = ?`, legacy); err != nil {
		t.Fatal(err)
	}
	if got, err := r.configuredZFSDataset(legacy, "tank/victim"); err != nil || got != "tank/restored" {
		t.Fatalf("explicit dataset = %q, %v", got, err)
	}
}

// An older imported item with no dataset restores to an explicitly chosen
// destination, and without one is refused with the error naming the fix.
func TestZFSLegacyImportHonoursExplicitDestination(t *testing.T) {
	t.Parallel()
	r, database, root := setupTestRunner(t)
	dest := createNamedLocalDest(t, database, "zfs-legacy-dest", root)
	legacy, err := database.CreateJob(db.Job{Name: "legacy", BackupTypeChain: "full", StorageDestID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AddJobItem(db.JobItem{JobID: legacy, ItemType: "zfs", ItemName: "tank/victim", Settings: `{}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateImportedJobRun(db.JobRun{JobID: legacy, Status: db.JobRunStatusImported, BackupType: "full"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	settings, err := r.zfsRestoreSettings(legacy, "tank/victim", "tank/chosen")
	if err != nil {
		t.Fatalf("settings with an explicit destination: %v", err)
	}
	if got, err := engine.ZFSRestoreTarget(settings); err != nil || got != "tank/chosen" {
		t.Fatalf("target with an explicit destination = %q, %v", got, err)
	}

	settings, err = r.zfsRestoreSettings(legacy, "tank/victim", "")
	if err != nil {
		t.Fatalf("settings without a destination: %v", err)
	}
	if _, ok := settings["dataset"]; ok {
		t.Fatalf("legacy item name became a target: %v", settings)
	}
	if got, err := engine.ZFSRestoreTarget(settings); err == nil || !strings.Contains(err.Error(), "no target dataset") {
		t.Fatalf("target without a destination = %q, %v, want a refusal", got, err)
	}
}

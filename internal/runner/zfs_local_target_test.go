package runner

import (
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
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

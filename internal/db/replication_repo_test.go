package db

import (
	"testing"
)

func TestReplicationSourceCRUD(t *testing.T) {
	database := setupTestDB(t)

	// Create a storage destination for the replication source to target.
	destID, err := database.CreateStorageDestination(StorageDestination{
		Name:   "repl-target",
		Type:   "local",
		Config: `{"path":"/tmp/repl"}`,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination() error = %v", err)
	}

	src := ReplicationSource{
		Name:          "prod-server",
		URL:           "http://192.168.1.10:24085",
		StorageDestID: destID,
		Schedule:      "0 */6 * * *",
		Enabled:       true,
	}

	// Create
	id, err := database.CreateReplicationSource(src)
	if err != nil {
		t.Fatalf("CreateReplicationSource() error = %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero ID")
	}

	// Get
	got, err := database.GetReplicationSource(id)
	if err != nil {
		t.Fatalf("GetReplicationSource() error = %v", err)
	}
	if got.Name != "prod-server" {
		t.Errorf("Name = %q, want %q", got.Name, "prod-server")
	}
	if got.URL != "http://192.168.1.10:24085" {
		t.Errorf("URL = %q, want %q", got.URL, "http://192.168.1.10:24085")
	}
	if got.StorageDestID != destID {
		t.Errorf("StorageDestID = %d, want %d", got.StorageDestID, destID)
	}
	if got.Schedule != "0 */6 * * *" {
		t.Errorf("Schedule = %q, want %q", got.Schedule, "0 */6 * * *")
	}
	if !got.Enabled {
		t.Error("Enabled = false, want true")
	}

	// List
	sources, err := database.ListReplicationSources()
	if err != nil {
		t.Fatalf("ListReplicationSources() error = %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("len(sources) = %d, want 1", len(sources))
	}

	// Update
	got.Name = "prod-server-v2"
	got.Schedule = "0 */12 * * *"
	if err := database.UpdateReplicationSource(got); err != nil {
		t.Fatalf("UpdateReplicationSource() error = %v", err)
	}
	updated, _ := database.GetReplicationSource(id)
	if updated.Name != "prod-server-v2" {
		t.Errorf("Name after update = %q, want %q", updated.Name, "prod-server-v2")
	}

	// Update sync status
	if err := database.UpdateReplicationSyncStatus(id, "success", ""); err != nil {
		t.Fatalf("UpdateReplicationSyncStatus() error = %v", err)
	}
	synced, _ := database.GetReplicationSource(id)
	if synced.LastSyncStatus != "success" {
		t.Errorf("LastSyncStatus = %q, want %q", synced.LastSyncStatus, "success")
	}
	if synced.LastSyncAt == nil {
		t.Error("LastSyncAt should not be nil after sync")
	}

	// Delete
	if err := database.DeleteReplicationSource(id); err != nil {
		t.Fatalf("DeleteReplicationSource() error = %v", err)
	}
	_, err = database.GetReplicationSource(id)
	if err != ErrNotFound {
		t.Errorf("GetReplicationSource after delete: got err=%v, want ErrNotFound", err)
	}
}

func TestReplicatedJobs(t *testing.T) {
	database := setupTestDB(t)

	// Create storage destination.
	destID, err := database.CreateStorageDestination(StorageDestination{
		Name:   "repl-storage",
		Type:   "local",
		Config: `{"path":"/tmp/repl"}`,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination() error = %v", err)
	}

	// Create replication source.
	srcID, err := database.CreateReplicationSource(ReplicationSource{
		Name:          "remote-vault",
		URL:           "http://10.0.0.1:24085",
		StorageDestID: destID,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("CreateReplicationSource() error = %v", err)
	}

	// Create a replicated job.
	jobID, err := database.CreateReplicatedJob(Job{
		Name:            "[remote-vault] Web Server",
		Description:     "Replicated from remote-vault",
		Enabled:         false,
		BackupTypeChain: "full",
		Compression:     "zstd",
		Encryption:      "none",
		ContainerMode:   "one_by_one",
		VMMode:          "snapshot",
		NotifyOn:        "failure",
		StorageDestID:   destID,
		SourceID:        srcID,
	})
	if err != nil {
		t.Fatalf("CreateReplicatedJob() error = %v", err)
	}

	// List replicated jobs for this source.
	jobs, err := database.ListReplicatedJobs(srcID)
	if err != nil {
		t.Fatalf("ListReplicatedJobs() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}
	if jobs[0].SourceID != srcID {
		t.Errorf("SourceID = %d, want %d", jobs[0].SourceID, srcID)
	}
	if jobs[0].ID != jobID {
		t.Errorf("ID = %d, want %d", jobs[0].ID, jobID)
	}

	// Verify GetJob also returns source_id.
	got, err := database.GetJob(jobID)
	if err != nil {
		t.Fatalf("GetJob() error = %v", err)
	}
	if got.SourceID != srcID {
		t.Errorf("GetJob().SourceID = %d, want %d", got.SourceID, srcID)
	}

	// Delete replicated jobs for the source.
	if err := database.DeleteReplicatedJobs(srcID); err != nil {
		t.Fatalf("DeleteReplicatedJobs() error = %v", err)
	}
	remaining, _ := database.ListReplicatedJobs(srcID)
	if len(remaining) != 0 {
		t.Errorf("len(remaining) = %d after delete, want 0", len(remaining))
	}
}

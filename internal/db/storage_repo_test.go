package db

import (
	"path/filepath"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestCreateAndGetStorageDestination(t *testing.T) {
	d := setupTestDB(t)
	dest := StorageDestination{Name: "local-backup", Type: "local", Config: `{"path":"/mnt/user/backups"}`}
	id, err := d.CreateStorageDestination(dest)
	if err != nil {
		t.Fatalf("Create error = %v", err)
	}
	got, err := d.GetStorageDestination(id)
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}
	if got.Name != "local-backup" {
		t.Errorf("Name = %q, want %q", got.Name, "local-backup")
	}
}

func TestListStorageDestinations(t *testing.T) {
	d := setupTestDB(t)
	d.CreateStorageDestination(StorageDestination{Name: "a", Type: "local", Config: "{}"})
	d.CreateStorageDestination(StorageDestination{Name: "b", Type: "smb", Config: "{}"})
	dests, err := d.ListStorageDestinations()
	if err != nil {
		t.Fatalf("List error = %v", err)
	}
	if len(dests) != 2 {
		t.Errorf("got %d destinations, want 2", len(dests))
	}
}

func TestDeleteStorageDestination(t *testing.T) {
	d := setupTestDB(t)
	id, _ := d.CreateStorageDestination(StorageDestination{Name: "del", Type: "local", Config: "{}"})
	if err := d.DeleteStorageDestination(id); err != nil {
		t.Fatalf("Delete error = %v", err)
	}
	_, err := d.GetStorageDestination(id)
	if err == nil {
		t.Error("Get after Delete should fail")
	}
}

func TestCountJobsByStorageDestID(t *testing.T) {
	d := setupTestDB(t)
	destID, _ := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: "{}"})
	otherID, _ := d.CreateStorageDestination(StorageDestination{Name: "other", Type: "local", Config: "{}"})

	tests := []struct {
		name      string
		setup     func()
		storageID int64
		want      int
	}{
		{
			name:      "no jobs",
			setup:     func() {},
			storageID: destID,
			want:      0,
		},
		{
			name: "two jobs on target, one on other",
			setup: func() {
				d.CreateJob(Job{Name: "job-a", StorageDestID: destID, BackupTypeChain: "full"})
				d.CreateJob(Job{Name: "job-b", StorageDestID: destID, BackupTypeChain: "full"})
				d.CreateJob(Job{Name: "job-c", StorageDestID: otherID, BackupTypeChain: "full"})
			},
			storageID: destID,
			want:      2,
		},
		{
			name:      "non-existent storage returns zero",
			setup:     func() {},
			storageID: 9999,
			want:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			got, err := d.CountJobsByStorageDestID(tt.storageID)
			if err != nil {
				t.Fatalf("CountJobsByStorageDestID() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("CountJobsByStorageDestID(%d) = %d, want %d", tt.storageID, got, tt.want)
			}
		})
	}
}

func TestRepointDedupChunksUpdatesPackOffsetLength(t *testing.T) {
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDedupPack(DedupPack{ID: "old", StorageID: destID, Path: "old.pack", SizeBytes: 100, ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDedupPack(DedupPack{ID: "new", StorageID: destID, Path: "new.pack", SizeBytes: 100, ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	cid := []byte{0xcc, 0x01, 0x02, 0x03}
	if err := d.UpsertDedupChunk(DedupChunk{ChunkID: cid, StorageID: destID, PackID: "old", Offset: 0, Length: 50}); err != nil {
		t.Fatal(err)
	}

	if err := d.RepointDedupChunks(destID, []DedupChunk{
		{ChunkID: cid, StorageID: destID, PackID: "new", Offset: 4096, Length: 60},
	}); err != nil {
		t.Fatal(err)
	}

	path, off, length, err := d.LocateDedupChunk(destID, cid)
	if err != nil {
		t.Fatal(err)
	}
	if path != "new.pack" || off != 4096 || length != 60 {
		t.Fatalf("re-point mismatch: got (%q, %d, %d)", path, off, length)
	}
}

func TestRepointDedupChunksEmptyIsNoop(t *testing.T) {
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RepointDedupChunks(destID, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.RepointDedupChunks(destID, []DedupChunk{}); err != nil {
		t.Fatal(err)
	}
}

// TestRepointDedupChunksFailsOnMissingChunk guards a dangerous silent
// failure mode: if a caller passes a stale or wrong chunkID, the UPDATE
// matches zero rows. The previous behaviour would commit nothing and
// return nil — and the compaction caller would then delete the old pack,
// losing the chunk forever. We require a hard error and a rolled-back
// transaction instead.
func TestRepointDedupChunksFailsOnMissingChunk(t *testing.T) {
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "d", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertDedupPack(DedupPack{ID: "new", StorageID: destID, Path: "new.pack", SizeBytes: 100, ChunkCount: 0}); err != nil {
		t.Fatal(err)
	}
	missing := []byte{0xff, 0xee}
	err = d.RepointDedupChunks(destID, []DedupChunk{
		{ChunkID: missing, StorageID: destID, PackID: "new", Offset: 1, Length: 1},
	})
	if err == nil {
		t.Fatal("RepointDedupChunks must error on missing chunk row, got nil")
	}
	// And the transaction must have rolled back — no row was inserted by accident.
	has, hasErr := d.HasDedupChunk(destID, missing)
	if hasErr != nil {
		t.Fatal(hasErr)
	}
	if has {
		t.Fatal("missing chunk row should not have been created by RepointDedupChunks")
	}
}

func TestUpdateStorageDestinationCapacityRoundTrip(t *testing.T) {
	d := setupTestDB(t)
	id, err := d.CreateStorageDestination(StorageDestination{
		Name: "cap-roundtrip", Type: "local", Config: `{"base_path":"/tmp/x"}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	cap := CapacityRecord{
		TotalBytes: 1 << 40, // 1 TiB
		UsedBytes:  1 << 30, // 1 GiB
		FreeBytes:  (1 << 40) - (1 << 30),
		ProbedAt:   now,
		Source:     "statfs",
	}
	if err := d.UpdateStorageDestinationCapacity(id, cap, ""); err != nil {
		t.Fatal(err)
	}

	got, err := d.GetStorageDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.CapacityTotalBytes == nil || *got.CapacityTotalBytes != cap.TotalBytes {
		t.Errorf("total = %v, want %d", got.CapacityTotalBytes, cap.TotalBytes)
	}
	if got.CapacitySource != "statfs" {
		t.Errorf("source = %q, want statfs", got.CapacitySource)
	}
	if got.CapacityError != "" {
		t.Errorf("error = %q, want empty", got.CapacityError)
	}
}

// TestDedupAggregatesLogicalBytesIncludesMultiItem is a regression test:
// multi-item dedup restore points have a NULL manifest_id (their per-item
// manifest IDs live in metadata.item_manifests), so a logical-bytes query
// that filtered on `manifest_id IS NOT NULL` reported 0 for container dedup
// jobs and the Storage card showed a bogus 0x dedup ratio. LogicalBytes must
// sum size_bytes across ALL of the destination's restore points.
func TestDedupAggregatesLogicalBytesIncludesMultiItem(t *testing.T) {
	d := setupTestDB(t)
	destID, err := d.CreateStorageDestination(StorageDestination{
		Name: "dedup", Type: "local", Config: `{"path":"/x"}`, DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := d.CreateJob(Job{Name: "j", StorageDestID: destID, BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(JobRun{JobID: jobID, Status: "completed", BackupType: "full"})
	if err != nil {
		t.Fatal(err)
	}
	// Multi-item style: manifest_id stays NULL, manifests in metadata.
	if _, err := d.CreateRestorePoint(RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "p/multi", SizeBytes: 1000,
		Metadata: `{"item_manifests":{"a":"00","b":"11"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	// Single-item style: manifest_id populated.
	singleID, err := d.CreateRestorePoint(RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: "p/single", SizeBytes: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetRestorePointManifestID(singleID, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}

	agg, err := d.DedupAggregates(destID)
	if err != nil {
		t.Fatalf("DedupAggregates: %v", err)
	}
	if agg.LogicalBytes != 3000 {
		t.Errorf("LogicalBytes = %d, want 3000 (multi-item NULL-manifest RP must be counted)", agg.LogicalBytes)
	}
}

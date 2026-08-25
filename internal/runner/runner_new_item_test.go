package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// TestNewItemInDifferentialJob verifies that when an existing backup job has a
// new item added to it, a subsequent incremental/differential backup treats the
// new item as a full capture (no changed_since) rather than inheriting the
// parent restore point's timestamp (issue #310).
func TestNewItemInDifferentialJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		chainType string
	}{
		{
			name:      "incremental job captures new item fully",
			chainType: "incremental",
		},
		{
			name:      "differential job captures new item fully",
			chainType: "differential",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, database, storageDir := setupTestRunner(t)
			dest := createLocalDest(t, database, storageDir)

			srcDir1 := t.TempDir()
			file1 := filepath.Join(srcDir1, "item1.txt")
			if err := os.WriteFile(file1, []byte("item 1 content"), 0o644); err != nil {
				t.Fatalf("write file1: %v", err)
			}

			jobID, err := database.CreateJob(db.Job{
				Name:            "test-job-" + tt.chainType,
				StorageDestID:   dest.ID,
				BackupTypeChain: tt.chainType,
				Enabled:         true,
			})
			if err != nil {
				t.Fatalf("create job: %v", err)
			}

			item1Settings, _ := json.Marshal(map[string]any{"path": srcDir1})
			if _, err := database.AddJobItem(db.JobItem{
				JobID:    jobID,
				ItemType: "folder",
				ItemName: "folder1",
				Settings: string(item1Settings),
			}); err != nil {
				t.Fatalf("add item1: %v", err)
			}

			// First run: full backup of folder1.
			r.RunJob(jobID)

			run1, err := database.GetJobRun(1)
			if err != nil {
				t.Fatalf("GetJobRun(1): %v", err)
			}
			if run1.Status != "completed" {
				t.Fatalf("run 1 status = %q, want completed", run1.Status)
			}

			rp1, err := database.GetRestorePoint(1)
			if err != nil {
				t.Fatalf("GetRestorePoint(1): %v", err)
			}
			members1, known1 := rp1.BackedUpItems()
			if !known1 {
				t.Fatal("expected known membership for run 1 restore point")
			}
			if _, ok := members1["folder1"]; !ok {
				t.Fatal("expected folder1 in run 1 restore point membership")
			}
			if _, ok := members1["folder2"]; ok {
				t.Fatal("folder2 should not be in run 1 restore point membership")
			}

			// Add second item to the job.
			srcDir2 := t.TempDir()
			file2 := filepath.Join(srcDir2, "item2.txt")
			wantContent := "item 2 content with preserved timestamp"
			if err := os.WriteFile(file2, []byte(wantContent), 0o644); err != nil {
				t.Fatalf("write file2: %v", err)
			}
			// Explicitly set mtime of file2 to the past (before run 1's restore point).
			pastTime := rp1.CreatedAt.Add(-2 * time.Hour)
			if err := os.Chtimes(file2, pastTime, pastTime); err != nil {
				t.Fatalf("chtimes file2: %v", err)
			}

			item2Settings, _ := json.Marshal(map[string]any{"path": srcDir2})
			if _, err := database.AddJobItem(db.JobItem{
				JobID:    jobID,
				ItemType: "folder",
				ItemName: "folder2",
				Settings: string(item2Settings),
			}); err != nil {
				t.Fatalf("add item2: %v", err)
			}

			// Second run: incremental/differential backup. folder1 is incremental; folder2 is new
			// and must receive a full capture.
			r.RunJob(jobID)

			run2, err := database.GetJobRun(2)
			if err != nil {
				t.Fatalf("GetJobRun(2): %v", err)
			}
			if run2.Status != "completed" {
				t.Fatalf("run 2 status = %q, want completed", run2.Status)
			}
			if run2.BackupType != tt.chainType {
				t.Fatalf("run 2 backup_type = %q, want %s", run2.BackupType, tt.chainType)
			}

			rp2, err := database.GetRestorePoint(2)
			if err != nil {
				t.Fatalf("GetRestorePoint(2): %v", err)
			}

			// Verify run 2 restore point recorded both items.
			members2, known2 := rp2.BackedUpItems()
			if !known2 {
				t.Fatal("expected known membership for run 2 restore point")
			}
			if _, ok := members2["folder1"]; !ok {
				t.Error("expected folder1 in run 2 restore point membership")
			}
			if _, ok := members2["folder2"]; !ok {
				t.Error("expected folder2 in run 2 restore point membership")
			}

			// Restore folder2 and verify its file content was captured completely despite stale mtime.
			restoreTarget := t.TempDir()
			if err := r.RestoreItem(rp2, "folder2", "folder", restoreTarget, ""); err != nil {
				t.Fatalf("RestoreItem folder2: %v", err)
			}
			restoredFile := filepath.Join(restoreTarget, "item2.txt")
			data, err := os.ReadFile(restoredFile)
			if err != nil {
				t.Fatalf("reading restored file: %v", err)
			}
			if string(data) != wantContent {
				t.Errorf("restored content = %q, want %q", string(data), wantContent)
			}
		})
	}
}

// TestRestoreItemNotPresentInChain verifies that restoring an item absent from
// all chain steps returns a descriptive error rather than silently succeeding.
func TestRestoreItemNotPresentInChain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		itemType string
	}{
		{name: "folder item absent from chain", itemType: "folder"},
		{name: "container item absent from chain", itemType: "container"},
		{name: "vm item absent from chain", itemType: "vm"},
		{name: "generic item absent from chain", itemType: "unknown-type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			database, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			t.Cleanup(func() { _ = database.Close() })

			storageDir := t.TempDir()
			storageConfig := fmt.Sprintf(`{"path":%q}`, storageDir)
			storageID, err := database.CreateStorageDestination(db.StorageDestination{
				Name:   "local",
				Type:   "local",
				Config: storageConfig,
			})
			if err != nil {
				t.Fatalf("CreateStorageDestination: %v", err)
			}

			jobID, err := database.CreateJob(db.Job{
				Name:            "chain-absent-test",
				Enabled:         true,
				BackupTypeChain: "incremental",
				StorageDestID:   storageID,
			})
			if err != nil {
				t.Fatalf("CreateJob: %v", err)
			}

			run1ID, err := database.CreateJobRun(db.JobRun{
				JobID:      jobID,
				Status:     "completed",
				BackupType: "full",
			})
			if err != nil {
				t.Fatalf("CreateJobRun 1: %v", err)
			}

			rp1ID, err := database.CreateRestorePoint(db.RestorePoint{
				JobID:       jobID,
				JobRunID:    run1ID,
				BackupType:  "full",
				StoragePath: "chain-absent-test/1_full",
				Metadata:    `{"item_sizes":{"other-item":100}}`,
				CreatedAt:   time.Now().Add(-time.Hour),
			})
			if err != nil {
				t.Fatalf("CreateRestorePoint 1: %v", err)
			}

			run2ID, err := database.CreateJobRun(db.JobRun{
				JobID:      jobID,
				Status:     "completed",
				BackupType: "incremental",
			})
			if err != nil {
				t.Fatalf("CreateJobRun 2: %v", err)
			}

			rp2ID, err := database.CreateRestorePoint(db.RestorePoint{
				JobID:                jobID,
				JobRunID:             run2ID,
				BackupType:           "incremental",
				StoragePath:          "chain-absent-test/2_inc",
				ParentRestorePointID: rp1ID,
				Metadata:             `{"item_sizes":{"other-item":100}}`,
				CreatedAt:            time.Now(),
			})
			if err != nil {
				t.Fatalf("CreateRestorePoint 2: %v", err)
			}

			rp2, err := database.GetRestorePoint(rp2ID)
			if err != nil {
				t.Fatalf("GetRestorePoint 2: %v", err)
			}

			r := New(database, ws.NewHub(), nil)
			err = r.RestoreItem(rp2, "missing-item", tt.itemType, t.TempDir(), "")
			if err == nil {
				t.Fatalf("RestoreItem should fail for missing item, got nil")
			}
			if !strings.Contains(err.Error(), "is not present in the restore chain") {
				t.Errorf("error = %q, want containing 'is not present in the restore chain'", err.Error())
			}
		})
	}
}

// TestStageContainerChainMergedSkipsHistoricalSteps verifies that
// stageContainerChainMerged successfully skips chain steps that did not capture
// the container item.
func TestStageContainerChainMergedSkipsHistoricalSteps(t *testing.T) {
	t.Parallel()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	storageDir := t.TempDir()
	storageConfig := fmt.Sprintf(`{"path":%q}`, storageDir)
	storageID, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "local",
		Type:   "local",
		Config: storageConfig,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}

	jobID, err := database.CreateJob(db.Job{
		Name:            "container-chain-test",
		Enabled:         true,
		BackupTypeChain: "incremental",
		StorageDestID:   storageID,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	adapter, err := storage.NewAdapter("local", storageConfig)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	t.Cleanup(func() { storage.CloseAdapter(adapter) })

	childTar := tarArchive(t, map[string]string{"data.txt": "hello world"})

	// Step 1 only had "other-container".
	// Step 2 has "new-container" with its files.
	childChecksums := writeStorageFiles(t, adapter, map[string]string{
		"container-chain-test/2_inc/new-container/config.json":   `{"Name":"new-container"}`,
		"container-chain-test/2_inc/new-container/image.tar":     "dummy image",
		"container-chain-test/2_inc/new-container/volume_0.tar": string(childTar),
	})

	baseRP := db.RestorePoint{
		ID:          1,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "container-chain-test/1_full",
		Metadata:    `{"item_sizes":{"other-container":500}}`,
		CreatedAt:   time.Now().Add(-time.Hour),
	}
	childRP := db.RestorePoint{
		ID:                   2,
		JobID:                jobID,
		BackupType:           "incremental",
		StoragePath:          "container-chain-test/2_inc",
		ParentRestorePointID: 1,
		Metadata:             restorePointMetadata("new-container", childChecksums),
		CreatedAt:            time.Now(),
	}

	r := New(database, ws.NewHub(), nil)
	tmpDir := t.TempDir()
	reporter := restoreProgressReporter{ItemName: "new-container", ItemType: "container", ItemsTotal: 1}

	mergedDir, err := r.stageContainerChainMerged(context.Background(), []db.RestorePoint{baseRP, childRP}, "new-container", "", reporter, tmpDir)
	if err != nil {
		t.Fatalf("stageContainerChainMerged: %v", err)
	}

	if _, err := os.Stat(filepath.Join(mergedDir, "config.json")); err != nil {
		t.Errorf("expected config.json in mergedDir: %v", err)
	}
}

// TestPruneChainResurrectedSkipsHistoricalSteps verifies that pruneChainResurrected
// skips steps that did not capture the item without error.
func TestPruneChainResurrectedSkipsHistoricalSteps(t *testing.T) {
	t.Parallel()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	storageDir := t.TempDir()
	storageConfig := fmt.Sprintf(`{"path":%q}`, storageDir)
	storageID, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "local",
		Type:   "local",
		Config: storageConfig,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}

	jobID, err := database.CreateJob(db.Job{
		Name:            "prune-test",
		Enabled:         true,
		BackupTypeChain: "incremental",
		StorageDestID:   storageID,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	adapter, err := storage.NewAdapter("local", storageConfig)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	t.Cleanup(func() { storage.CloseAdapter(adapter) })

	// Write listing for step 2 (new-item).
	listingJSON, err := json.Marshal(engine.TarIndex{
		Version: 1,
		Files:   []engine.TarIndexEntry{{Path: "file1.txt", Size: 10}},
	})
	if err != nil {
		t.Fatalf("marshal listing: %v", err)
	}
	_ = writeStorageFiles(t, adapter, map[string]string{
		"prune-test/2_inc/new-item/data.tar.listing.json": string(listingJSON),
	})

	baseRP := db.RestorePoint{
		ID:          1,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "prune-test/1_full",
		Metadata:    `{"item_sizes":{"other-item":500}}`,
		CreatedAt:   time.Now().Add(-time.Hour),
	}
	childRP := db.RestorePoint{
		ID:                   2,
		JobID:                jobID,
		BackupType:           "incremental",
		StoragePath:          "prune-test/2_inc",
		ParentRestorePointID: 1,
		Metadata:             `{"item_sizes":{"new-item":10}}`,
		CreatedAt:            time.Now(),
	}

	r := New(database, ws.NewHub(), nil)
	destDir := t.TempDir()

	// Should execute smoothly and skip step 1.
	r.pruneChainResurrected([]db.RestorePoint{baseRP, childRP}, "new-item", destDir, "", time.Now())
}

// TestRestoreMergedChainGenericSkipsHistoricalSteps verifies that restoreMergedChain
// (the non-VM / non-container branch) skips chain steps that did not capture the item.
func TestRestoreMergedChainGenericSkipsHistoricalSteps(t *testing.T) {
	t.Parallel()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	storageDir := t.TempDir()
	storageConfig := fmt.Sprintf(`{"path":%q}`, storageDir)
	storageID, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "local",
		Type:   "local",
		Config: storageConfig,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}

	jobID, err := database.CreateJob(db.Job{
		Name:            "generic-chain-test",
		Enabled:         true,
		BackupTypeChain: "incremental",
		StorageDestID:   storageID,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	adapter, err := storage.NewAdapter("local", storageConfig)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	t.Cleanup(func() { storage.CloseAdapter(adapter) })

	childChecksums := writeStorageFiles(t, adapter, map[string]string{
		"generic-chain-test/2_inc/custom-item/payload.txt": "custom payload",
	})

	baseRP := db.RestorePoint{
		ID:          1,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "generic-chain-test/1_full",
		Metadata:    `{"item_sizes":{"other-item":500}}`,
		CreatedAt:   time.Now().Add(-time.Hour),
	}
	childRP := db.RestorePoint{
		ID:                   2,
		JobID:                jobID,
		BackupType:           "incremental",
		StoragePath:          "generic-chain-test/2_inc",
		ParentRestorePointID: 1,
		Metadata:             restorePointMetadata("custom-item", childChecksums),
		CreatedAt:            time.Now(),
	}

	r := New(database, ws.NewHub(), nil)
	reporter := restoreProgressReporter{ItemName: "custom-item", ItemType: "custom", ItemsTotal: 1}

	// restoreMergedChain for generic type will stage step 2 (skipping step 1) and invoke restoreStagedItem.
	// We expect restoreStagedItem to fail on unknown handler, confirming staging of step 2 succeeded and step 1 was skipped.
	err = r.restoreMergedChain(context.Background(), []db.RestorePoint{baseRP, childRP}, "custom-item", "custom", t.TempDir(), "", nil, reporter)
	if err == nil {
		t.Fatalf("expected error from unknown item type handler, got nil")
	}
	if strings.Contains(err.Error(), "is not present in the restore chain") {
		t.Errorf("step 2 should have been found, got %v", err)
	}
}


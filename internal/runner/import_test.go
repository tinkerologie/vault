package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaandeysel/vault/internal/db"
	"github.com/ruaandeysel/vault/internal/storage"
)

func setupTestRunner(t *testing.T) (*Runner, *db.DB, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	storageDir := t.TempDir()
	r := New(database, nil, nil)
	return r, database, storageDir
}

func createLocalDest(t *testing.T, database *db.DB, storageDir string) db.StorageDestination {
	t.Helper()
	cfg := `{"path":"` + strings.ReplaceAll(storageDir, `\`, `\\`) + `"}`
	id, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "test-local",
		Type:   "local",
		Config: cfg,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}
	dest, _ := database.GetStorageDestination(id)
	return dest
}

func TestImportBackups(t *testing.T) {
	t.Parallel()

	t.Run("imports new job and restore point", func(t *testing.T) {
		t.Parallel()
		r, database, storageDir := setupTestRunner(t)
		dest := createLocalDest(t, database, storageDir)

		backups := []map[string]any{
			{
				"job_name":     "my-containers",
				"storage_path": "vault/my-containers/1_2026-01-15_020000",
				"backup_type":  "full",
				"size_bytes":   float64(1024000),
				"compression":  "zstd",
				"items_done":   float64(3),
			},
		}

		imported, err := r.ImportBackups(dest.ID, backups)
		if err != nil {
			t.Fatalf("ImportBackups error = %v", err)
		}
		if imported != 1 {
			t.Errorf("imported = %d, want 1", imported)
		}

		// Verify job was created.
		job, err := database.GetJobByName("my-containers")
		if err != nil {
			t.Fatalf("GetJobByName error = %v", err)
		}
		if job.Enabled {
			t.Error("imported job should be disabled")
		}
		if job.StorageDestID != dest.ID {
			t.Errorf("StorageDestID = %d, want %d", job.StorageDestID, dest.ID)
		}

		// Verify restore point was created.
		rps, err := database.ListRestorePoints(job.ID)
		if err != nil {
			t.Fatalf("ListRestorePoints error = %v", err)
		}
		if len(rps) != 1 {
			t.Fatalf("got %d restore points, want 1", len(rps))
		}
		if rps[0].StoragePath != "vault/my-containers/1_2026-01-15_020000" {
			t.Errorf("StoragePath = %q", rps[0].StoragePath)
		}
		if rps[0].SizeBytes != 1024000 {
			t.Errorf("SizeBytes = %d, want 1024000", rps[0].SizeBytes)
		}
	})

	t.Run("deduplicates by storage_path", func(t *testing.T) {
		t.Parallel()
		r, database, storageDir := setupTestRunner(t)
		dest := createLocalDest(t, database, storageDir)

		backups := []map[string]any{
			{"job_name": "dedup-test", "storage_path": "vault/dedup-test/1_run", "backup_type": "full"},
		}

		imported1, _ := r.ImportBackups(dest.ID, backups)
		imported2, _ := r.ImportBackups(dest.ID, backups)

		if imported1 != 1 {
			t.Errorf("first import = %d, want 1", imported1)
		}
		if imported2 != 0 {
			t.Errorf("duplicate import = %d, want 0", imported2)
		}
	})

	t.Run("skips entries without job_name", func(t *testing.T) {
		t.Parallel()
		r, database, storageDir := setupTestRunner(t)
		dest := createLocalDest(t, database, storageDir)

		backups := []map[string]any{
			{"storage_path": "vault/orphan/1_run", "backup_type": "full"},
			{"job_name": "", "storage_path": "vault/empty/1_run"},
			{"job_name": "valid", "storage_path": "vault/valid/1_run", "backup_type": "full"},
		}

		imported, _ := r.ImportBackups(dest.ID, backups)
		if imported != 1 {
			t.Errorf("imported = %d, want 1", imported)
		}
	})

	t.Run("reuses existing job by name", func(t *testing.T) {
		t.Parallel()
		r, database, storageDir := setupTestRunner(t)
		dest := createLocalDest(t, database, storageDir)

		// Pre-create a job.
		database.CreateJob(db.Job{
			Name: "existing-job", Enabled: true,
			BackupTypeChain: "full", StorageDestID: dest.ID,
		})

		backups := []map[string]any{
			{"job_name": "existing-job", "storage_path": "vault/existing-job/1_run", "backup_type": "full"},
		}
		imported, _ := r.ImportBackups(dest.ID, backups)
		if imported != 1 {
			t.Errorf("imported = %d, want 1", imported)
		}

		// Should still only have one job.
		jobs, _ := database.ListJobs()
		if len(jobs) != 1 {
			t.Errorf("got %d jobs, want 1", len(jobs))
		}
	})
}

func TestScanStorageManifests(t *testing.T) {
	t.Parallel()

	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	// Create a realistic directory structure:
	// vault/my-job/1_2026-01-15_020000/manifest.json
	// vault/my-job/2_2026-01-16_020000/manifest.json
	// vault/other-job/3_2026-01-17_020000/ (no manifest — should be skipped)
	runs := []struct {
		dir      string
		manifest map[string]any
	}{
		{
			dir: filepath.Join(storageDir, "vault", "my-job", "1_2026-01-15_020000"),
			manifest: map[string]any{
				"version": float64(1), "job_name": "my-job", "backup_type": "full",
				"size_bytes": float64(500), "created_at": "2026-01-15T02:00:00Z",
			},
		},
		{
			dir: filepath.Join(storageDir, "vault", "my-job", "2_2026-01-16_020000"),
			manifest: map[string]any{
				"version": float64(1), "job_name": "my-job", "backup_type": "incremental",
				"size_bytes": float64(100), "created_at": "2026-01-16T02:00:00Z",
			},
		},
	}

	for _, run := range runs {
		if err := os.MkdirAll(run.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(run.manifest)
		if err := os.WriteFile(filepath.Join(run.dir, "manifest.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Create a run directory without manifest.
	if err := os.MkdirAll(filepath.Join(storageDir, "vault", "other-job", "3_2026-01-17_020000"), 0o755); err != nil {
		t.Fatal(err)
	}

	manifests, err := r.ScanStorageManifests(dest)
	if err != nil {
		t.Fatalf("ScanStorageManifests error = %v", err)
	}
	if len(manifests) != 2 {
		t.Fatalf("got %d manifests, want 2", len(manifests))
	}

	// Each manifest should have storage_path injected.
	for _, m := range manifests {
		sp, ok := m["storage_path"].(string)
		if !ok || sp == "" {
			t.Errorf("manifest missing storage_path: %v", m)
		}
	}
}

func TestDeleteStorageDir(t *testing.T) {
	t.Parallel()

	r, _, storageDir := setupTestRunner(t)

	// Create files to delete.
	dir := filepath.Join(storageDir, "vault", "test-job", "run1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file1.tar.zst"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	adapter := storage.NewLocalAdapter(storageDir)
	r.DeleteStorageDir(adapter, "vault/test-job/run1")

	// Verify files are gone.
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d remaining entries, want 0", len(entries))
	}
}

func TestCleanupJobStorage(t *testing.T) {
	t.Parallel()

	r, database, storageDir := setupTestRunner(t)
	dest := createLocalDest(t, database, storageDir)

	// Create a job with a restore point.
	jobID, _ := database.CreateJob(db.Job{
		Name: "cleanup-test", BackupTypeChain: "full", StorageDestID: dest.ID,
	})
	runID, _ := database.CreateJobRun(db.JobRun{
		JobID: jobID, Status: "success", BackupType: "full",
	})
	database.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "vault/cleanup-test/1_2026-01-01_020000",
		Metadata: "{}", SizeBytes: 100,
	})

	// Create the storage files.
	runDir := filepath.Join(storageDir, "vault", "cleanup-test", "1_2026-01-01_020000")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "data.tar"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.CleanupJobStorage(jobID); err != nil {
		t.Fatalf("CleanupJobStorage error = %v", err)
	}

	// Verify the run directory files are gone.
	if _, err := os.Stat(filepath.Join(runDir, "data.tar")); !os.IsNotExist(err) {
		t.Error("data.tar should have been deleted")
	}
}

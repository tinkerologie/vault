package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/ws"
)

func TestStageRestorePointItemOverlaysChainFiles(t *testing.T) {
	t.Parallel()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	storageRoot := t.TempDir()
	storageConfig := fmt.Sprintf(`{"path":%q}`, storageRoot)
	storageID, err := database.CreateStorageDestination(db.StorageDestination{
		Name:   "local",
		Type:   "local",
		Config: storageConfig,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}

	jobID, err := database.CreateJob(db.Job{
		Name:            "chain-test",
		Enabled:         true,
		BackupTypeChain: "incremental",
		Compression:     "none",
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

	baseChecksums := writeStorageFiles(t, adapter, map[string]string{
		"chain-test/1_full/my-item/config.json":     "base-config",
		"chain-test/1_full/my-item/image.tar":       "base-image",
		"chain-test/1_full/my-item/volume_0.tar.gz": "base-volume",
	})
	childChecksums := writeStorageFiles(t, adapter, map[string]string{
		"chain-test/2_inc/my-item/config.json":     "child-config",
		"chain-test/2_inc/my-item/volume_0.tar.gz": "child-volume",
	})

	baseRP := db.RestorePoint{
		ID:          1,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "chain-test/1_full",
		Metadata:    restorePointMetadata("my-item", baseChecksums),
		CreatedAt:   time.Now().Add(-time.Hour),
	}
	childRP := db.RestorePoint{
		ID:                   2,
		JobID:                jobID,
		BackupType:           "incremental",
		StoragePath:          "chain-test/2_inc",
		Metadata:             restorePointMetadata("my-item", childChecksums),
		ParentRestorePointID: 1,
		CreatedAt:            time.Now(),
	}

	r := New(database, ws.NewHub(), nil)
	tmpDir := t.TempDir()
	reporter := restoreProgressReporter{ItemName: "my-item", ItemType: "container", ItemsTotal: 1}

	if err := r.stageRestorePointItem(context.Background(), baseRP, "my-item", tmpDir, "", 0, 50, reporter); err != nil {
		t.Fatalf("stageRestorePointItem(base) error = %v", err)
	}
	if err := r.stageRestorePointItem(context.Background(), childRP, "my-item", tmpDir, "", 50, 100, reporter); err != nil {
		t.Fatalf("stageRestorePointItem(child) error = %v", err)
	}

	assertFileContents(t, tmpDir, "config.json", "child-config")
	assertFileContents(t, tmpDir, "image.tar", "base-image")
	assertFileContents(t, tmpDir, "volume_0.tar.gz", "child-volume")
}

// TestStageRestorePointItemMissingItemDirectory verifies that restoring an
// item whose chain includes a restore point created before the item existed
// (no item directory on storage) skips that step gracefully with a warning
// rather than failing the restore with ENOENT (issue #355).
func TestStageRestorePointItemMissingItemDirectory(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	storageRoot := t.TempDir()
	dest := createLocalDest(t, d, storageRoot)

	jobID, err := d.CreateJob(db.Job{
		Name:            "chain-test-missing-dir",
		Enabled:         true,
		BackupTypeChain: "incremental",
		Compression:     "none",
		StorageDestID:   dest.ID,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "incremental"})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	t.Cleanup(func() { storage.CloseAdapter(adapter) })

	// Step 1 (full): predates "new-item"; contains "other-item" only.
	// The directory "chain-test-missing-dir/1_full/new-item" does NOT exist.
	_ = writeStorageFiles(t, adapter, map[string]string{
		"chain-test-missing-dir/1_full/other-item/config.json": "other-config",
	})
	// Step 2 (inc): "new-item" was added to the job.
	step2Checksums := writeStorageFiles(t, adapter, map[string]string{
		"chain-test-missing-dir/2_inc/new-item/config.json":     "step2-config",
		"chain-test-missing-dir/2_inc/new-item/volume_0.tar.gz": "step2-volume",
	})
	// Step 3 (inc): updated "new-item".
	step3Checksums := writeStorageFiles(t, adapter, map[string]string{
		"chain-test-missing-dir/3_inc/new-item/volume_0.tar.gz": "step3-volume",
	})

	baseRP := db.RestorePoint{
		ID:          1,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "chain-test-missing-dir/1_full",
		Metadata:    restorePointMetadata("other-item", map[string]string{"config.json": checksumString("other-config")}),
		CreatedAt:   time.Now().Add(-2 * time.Hour),
	}
	step2RP := db.RestorePoint{
		ID:                   2,
		JobID:                jobID,
		BackupType:           "incremental",
		StoragePath:          "chain-test-missing-dir/2_inc",
		Metadata:             restorePointMetadata("new-item", step2Checksums),
		ParentRestorePointID: 1,
		CreatedAt:            time.Now().Add(-time.Hour),
	}
	step3RP := db.RestorePoint{
		ID:                   3,
		JobID:                jobID,
		BackupType:           "incremental",
		StoragePath:          "chain-test-missing-dir/3_inc",
		Metadata:             restorePointMetadata("new-item", step3Checksums),
		ParentRestorePointID: 2,
		CreatedAt:            time.Now(),
	}

	tmpDir := t.TempDir()
	reporter := restoreProgressReporter{RunID: runID, ItemName: "new-item", ItemType: "container", ItemsTotal: 1}

	// 1. Staging the missing step should return nil (no hard error).
	if err := r.stageRestorePointItem(context.Background(), baseRP, "new-item", tmpDir, "", 0, 33, reporter); err != nil {
		t.Fatalf("stageRestorePointItem(base missing) unexpected error = %v", err)
	}

	// 2. A "No restore data found" warning must be logged in run log.
	entries, err := d.ListRunLogEntries(context.Background(), runID, 0, 100)
	if err != nil {
		t.Fatalf("ListRunLogEntries: %v", err)
	}
	wantWarning := fmt.Sprintf("No restore data found for new-item (restore point %d)", baseRP.ID)
	var foundWarning bool
	for _, e := range entries {
		if e.Level == "warn" && strings.Contains(e.Message, wantWarning) {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Errorf("expected warning run-log entry containing %q; got:\n%+v", wantWarning, entries)
	}

	// 3. Staging subsequent steps that do have the item succeeds and stages correctly.
	if err := r.stageRestorePointItem(context.Background(), step2RP, "new-item", tmpDir, "", 33, 66, reporter); err != nil {
		t.Fatalf("stageRestorePointItem(step2) unexpected error = %v", err)
	}
	if err := r.stageRestorePointItem(context.Background(), step3RP, "new-item", tmpDir, "", 66, 100, reporter); err != nil {
		t.Fatalf("stageRestorePointItem(step3) unexpected error = %v", err)
	}

	assertFileContents(t, tmpDir, "config.json", "step2-config")
	assertFileContents(t, tmpDir, "volume_0.tar.gz", "step3-volume")

	// 4. Confirm full container chain staging (which loops over baseRP, step2RP, step3RP)
	// does not abort with ENOENT.
	fullChainDir := t.TempDir()
	fullFullTar := tarArchive(t, map[string]string{"config.json": "step2-config"})
	fullDiffTar := tarArchive(t, map[string]string{"data.txt": "data"})
	writeStorageFiles(t, adapter, map[string]string{
		"chain-test-missing-dir/2_inc/chain-container/volume_0.tar": string(fullFullTar),
		"chain-test-missing-dir/3_inc/chain-container/volume_0.tar": string(fullDiffTar),
	})
	chainReporter := restoreProgressReporter{RunID: runID, ItemName: "chain-container", ItemType: "container", ItemsTotal: 1}
	mergedDir, err := r.stageContainerChainMerged(context.Background(), []db.RestorePoint{baseRP, step2RP, step3RP}, "chain-container", "", chainReporter, fullChainDir)
	if err != nil {
		t.Fatalf("stageContainerChainMerged with missing intermediate directory failed: %v", err)
	}
	names := tarEntryNames(t, filepath.Join(mergedDir, "volume_0.tar"))
	if !names["config.json"] || !names["data.txt"] {
		t.Errorf("merged volume missing entries, got: %+v", names)
	}
}

// TestStageContainerChainMerged exercises the container branch of
// restoreMergedChain's per-step staging + merge (the new code path for
// issue #320) without requiring a Docker daemon. The full step's volume_0.tar
// holds old.txt; the differential step's volume_0.tar holds only new.txt. The
// merged staging dir must contain a single volume_0.tar with BOTH files, so a
// later ContainerHandler.Restore would restore the complete volume.
func TestStageContainerChainMerged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		wantInMerged []string
	}{
		{
			name:         "differential step's partial volume overlays the full step's",
			wantInMerged: []string{"old.txt", "new.txt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			t.Cleanup(func() { _ = database.Close() })

			storageRoot := t.TempDir()
			storageConfig := fmt.Sprintf(`{"path":%q}`, storageRoot)
			storageID, err := database.CreateStorageDestination(db.StorageDestination{
				Name:   "local",
				Type:   "local",
				Config: storageConfig,
			})
			if err != nil {
				t.Fatalf("CreateStorageDestination: %v", err)
			}

			jobID, err := database.CreateJob(db.Job{
				Name:            "chain-test",
				Enabled:         true,
				BackupTypeChain: "incremental",
				Compression:     "none",
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

			fullTar := tarArchive(t, map[string]string{"old.txt": "old"})
			diffTar := tarArchive(t, map[string]string{"new.txt": "new"})

			baseChecksums := writeStorageFiles(t, adapter, map[string]string{
				"chain-test/1_full/my-item/volume_0.tar": string(fullTar),
			})
			childChecksums := writeStorageFiles(t, adapter, map[string]string{
				"chain-test/2_inc/my-item/volume_0.tar": string(diffTar),
			})

			baseRP := db.RestorePoint{
				ID:          1,
				JobID:       jobID,
				BackupType:  "full",
				StoragePath: "chain-test/1_full",
				Metadata:    restorePointMetadata("my-item", baseChecksums),
				CreatedAt:   time.Now().Add(-time.Hour),
			}
			childRP := db.RestorePoint{
				ID:                   2,
				JobID:                jobID,
				BackupType:           "incremental",
				StoragePath:          "chain-test/2_inc",
				Metadata:             restorePointMetadata("my-item", childChecksums),
				ParentRestorePointID: 1,
				CreatedAt:            time.Now(),
			}

			r := New(database, ws.NewHub(), nil)
			tmpDir := t.TempDir()
			reporter := restoreProgressReporter{ItemName: "my-item", ItemType: "container", ItemsTotal: 1}

			mergedDir, err := r.stageContainerChainMerged(context.Background(), []db.RestorePoint{baseRP, childRP}, "my-item", "", reporter, tmpDir)
			if err != nil {
				t.Fatalf("stageContainerChainMerged: %v", err)
			}

			names := tarEntryNames(t, filepath.Join(mergedDir, "volume_0.tar"))
			for _, want := range tt.wantInMerged {
				if !names[want] {
					t.Errorf("merged volume missing %s", want)
				}
			}
		})
	}
}

// TestStageContainerChainMergedRunLog asserts that classic container chain
// staging mirrors the VM and flat folder chain paths by emitting a
// structured "Staging chain step X/Y" entry into the restore run log for
// each step (issue #320 review follow-up: the container branch previously
// logged via log.Printf only, losing these user-facing run-log lines).
func TestStageContainerChainMergedRunLog(t *testing.T) {
	t.Parallel()
	r, d := newTestRunner(t)

	storageRoot := t.TempDir()
	storageConfig := fmt.Sprintf(`{"path":%q}`, storageRoot)
	storageID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:   "local",
		Type:   "local",
		Config: storageConfig,
	})
	if err != nil {
		t.Fatalf("CreateStorageDestination: %v", err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name:            "chain-test-runlog",
		Enabled:         true,
		BackupTypeChain: "incremental",
		Compression:     "none",
		StorageDestID:   storageID,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// runLog persists to run_log_entries, which carries a foreign key on
	// runs — seed a real job run so the staged steps can be stored.
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "running", BackupType: "full"})
	if err != nil {
		t.Fatalf("CreateJobRun: %v", err)
	}

	adapter, err := storage.NewAdapter("local", storageConfig)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	t.Cleanup(func() { storage.CloseAdapter(adapter) })

	fullTar := tarArchive(t, map[string]string{"old.txt": "old"})
	diffTar := tarArchive(t, map[string]string{"new.txt": "new"})

	baseChecksums := writeStorageFiles(t, adapter, map[string]string{
		"chain-test/1_full/my-item/volume_0.tar": string(fullTar),
	})
	childChecksums := writeStorageFiles(t, adapter, map[string]string{
		"chain-test/2_inc/my-item/volume_0.tar": string(diffTar),
	})

	baseRP := db.RestorePoint{
		ID:          1,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "chain-test/1_full",
		Metadata:    restorePointMetadata("my-item", baseChecksums),
		CreatedAt:   time.Now().Add(-time.Hour),
	}
	childRP := db.RestorePoint{
		ID:                   2,
		JobID:                jobID,
		BackupType:           "incremental",
		StoragePath:          "chain-test/2_inc",
		Metadata:             restorePointMetadata("my-item", childChecksums),
		ParentRestorePointID: 1,
		CreatedAt:            time.Now(),
	}

	tmpDir := t.TempDir()
	reporter := restoreProgressReporter{RunID: runID, ItemName: "my-item", ItemType: "container", ItemsTotal: 1}

	if _, err := r.stageContainerChainMerged(context.Background(), []db.RestorePoint{baseRP, childRP}, "my-item", "", reporter, tmpDir); err != nil {
		t.Fatalf("stageContainerChainMerged: %v", err)
	}

	entries, err := d.ListRunLogEntries(context.Background(), runID, 0, 100)
	if err != nil {
		t.Fatalf("ListRunLogEntries: %v", err)
	}
	var msgs []string
	for _, e := range entries {
		msgs = append(msgs, e.Message)
	}
	joined := strings.Join(msgs, "\n")

	// One row per chain step: the merged container staging must narrate
	// each step with the same structured run-log line the VM and folder
	// chain paths already emit.
	cases := []struct {
		name string
		want string
	}{
		{name: "full step staged", want: "Staging chain step 1/2 (type=full, id=1)"},
		{name: "incremental step staged", want: "Staging chain step 2/2 (type=incremental, id=2)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(joined, tc.want) {
				t.Errorf("run log missing %q; got:\n%s", tc.want, joined)
			}
		})
	}
}

// tarArchive builds an uncompressed tar archive from a name→content map.
func tarArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tarEntryNames returns the set of regular-file entry names in an
// uncompressed tar archive.
func tarEntryNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	names := map[string]bool{}
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading tar %s: %v", path, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			names[hdr.Name] = true
		}
	}
	return names
}

func TestProtectedRestorePointIDsKeepsAncestors(t *testing.T) {
	t.Parallel()

	now := time.Now()
	points := []db.RestorePoint{
		{ID: 3, BackupType: "incremental", ParentRestorePointID: 2, CreatedAt: now},
		{ID: 2, BackupType: "full", CreatedAt: now.Add(-24 * time.Hour)},
		{ID: 1, BackupType: "full", CreatedAt: now.Add(-48 * time.Hour)},
	}

	protected := protectedRestorePointIDs(points, 1, 1, now)
	if _, ok := protected[3]; !ok {
		t.Fatal("expected latest restore point to be protected")
	}
	if _, ok := protected[2]; !ok {
		t.Fatal("expected parent restore point to be protected")
	}
	if _, ok := protected[1]; ok {
		t.Fatal("expected unrelated old restore point to be deletable")
	}
}

func writeStorageFiles(t *testing.T, adapter storage.Adapter, files map[string]string) map[string]string {
	t.Helper()

	checksums := make(map[string]string, len(files))
	for path, content := range files {
		if err := adapter.Write(path, strings.NewReader(content)); err != nil {
			t.Fatalf("adapter.Write(%s): %v", path, err)
		}
		checksums[path[strings.LastIndex(path, "/")+1:]] = checksumString(content)
	}
	return checksums
}

func restorePointMetadata(itemName string, checksums map[string]string) string {
	payload, _ := json.Marshal(map[string]any{
		"checksums": map[string]any{
			itemName: checksums,
		},
	})
	return string(payload)
}

func checksumString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func assertFileContents(t *testing.T, dir, name, want string) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", name, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", name, string(data), want)
	}
}

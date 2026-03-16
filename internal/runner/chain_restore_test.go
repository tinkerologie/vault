package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	if err := r.stageRestorePointItem(baseRP, "my-item", tmpDir, "", 0, 50, reporter); err != nil {
		t.Fatalf("stageRestorePointItem(base) error = %v", err)
	}
	if err := r.stageRestorePointItem(childRP, "my-item", tmpDir, "", 50, 100, reporter); err != nil {
		t.Fatalf("stageRestorePointItem(child) error = %v", err)
	}

	assertFileContents(t, tmpDir, "config.json", "child-config")
	assertFileContents(t, tmpDir, "image.tar", "base-image")
	assertFileContents(t, tmpDir, "volume_0.tar.gz", "child-volume")
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

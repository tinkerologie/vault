package mount

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/ws"
)

func TestManager_NonDedupFails(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:         "classic-dest",
		Type:         "local",
		Config:       `{"path":"/tmp/classic"}`,
		DedupEnabled: false, // Not dedup!
	})
	if err != nil {
		t.Fatal(err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name:            "classic-job",
		StorageDestID:   destID,
		BackupTypeChain: "full",
	})
	if err != nil {
		t.Fatal(err)
	}

	runID, err := d.CreateJobRun(db.JobRun{
		JobID:      jobID,
		Status:     "completed",
		BackupType: "full",
	})
	if err != nil {
		t.Fatal(err)
	}

	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "backups/test.tar.zst",
	})
	if err != nil {
		t.Fatal(err)
	}

	hub := ws.NewHub()
	serverKey := bytes.Repeat([]byte{0xaa}, dedup.SecretSize)
	mgr := NewManager(d, hub, serverKey)

	_, err = mgr.MountRestorePoint(context.Background(), jobID, rpID)
	if err == nil {
		t.Fatal("expected error mounting non-dedup restore point, got nil")
	}
	if err.Error() != "mount: only deduplicated backups support random-access FUSE mounting" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestManager_MissingManifestFails(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:         "dedup-dest",
		Type:         "local",
		Config:       `{"path":"` + dir + `"}`,
		DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	jobID, err := d.CreateJob(db.Job{
		Name:            "dedup-job",
		StorageDestID:   destID,
		BackupTypeChain: "full",
	})
	if err != nil {
		t.Fatal(err)
	}

	runID, err := d.CreateJobRun(db.JobRun{
		JobID:      jobID,
		Status:     "completed",
		BackupType: "full",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Restore point with empty metadata and no ManifestID
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID:    runID,
		JobID:       jobID,
		BackupType:  "full",
		StoragePath: "backups/test",
		Metadata:    `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	hub := ws.NewHub()
	serverKey := bytes.Repeat([]byte{0xbb}, dedup.SecretSize)

	adapter, err := storage.NewAdapter("local", `{"path":"`+dir+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dedup.InitRepo(d, adapter, destID, serverKey); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(d, hub, serverKey)

	_, err = mgr.MountRestorePoint(context.Background(), jobID, rpID)
	if err == nil {
		t.Fatal("expected error on missing manifest, got nil")
	}
	if !strings.Contains(err.Error(), "mount: no manifests found in restore point") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManager_IdleTimeoutConfiguration(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	mgr := NewManager(d, nil, nil)
	// Default should be 30m
	if to := mgr.getIdleTimeout(); to != 30*time.Minute {
		t.Errorf("default timeout = %v, want 30m", to)
	}

	// Set custom timeout
	_ = d.SetSetting("fuse_mount_idle_minutes", "15")
	if to := mgr.getIdleTimeout(); to != 15*time.Minute {
		t.Errorf("custom timeout = %v, want 15m", to)
	}

	// Disable timeout
	_ = d.SetSetting("fuse_mount_idle_minutes", "0")
	if to := mgr.getIdleTimeout(); to != 0 {
		t.Errorf("disabled timeout = %v, want 0", to)
	}
}

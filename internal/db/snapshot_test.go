package db

import (
	"path/filepath"
	"testing"
)

func TestSnapshotRoundTrip(t *testing.T) {
	// Open a DB and insert data.
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.db")
	snapshotPath := filepath.Join(dir, "snapshots", "vault.db")

	srcDB, err := Open(srcPath)
	if err != nil {
		t.Fatalf("open source DB: %v", err)
	}

	_, err = srcDB.CreateStorageDestination(StorageDestination{
		Name:   "test-dest",
		Type:   "local",
		Config: `{"path":"/mnt/backups"}`,
	})
	if err != nil {
		t.Fatalf("insert storage destination: %v", err)
	}

	// Save a snapshot.
	sm := NewSnapshotManager(srcDB, snapshotPath)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	srcDB.Close()

	// Open a fresh DB and restore the snapshot into it.
	freshPath := filepath.Join(dir, "fresh.db")
	freshDB, err := Open(freshPath)
	if err != nil {
		t.Fatalf("open fresh DB: %v", err)
	}

	sm2 := NewSnapshotManager(freshDB, snapshotPath)
	if err := sm2.RestoreFromSnapshot(); err != nil {
		t.Fatalf("RestoreFromSnapshot: %v", err)
	}
	freshDB.Close()

	// Reopen and verify the data survived.
	reopened, err := Open(freshPath)
	if err != nil {
		t.Fatalf("reopen DB: %v", err)
	}
	defer reopened.Close()

	dests, err := reopened.ListStorageDestinations()
	if err != nil {
		t.Fatalf("list storage destinations: %v", err)
	}
	if len(dests) != 1 {
		t.Fatalf("got %d destinations, want 1", len(dests))
	}
	if dests[0].Name != "test-dest" {
		t.Errorf("Name = %q, want %q", dests[0].Name, "test-dest")
	}
}

func TestSnapshotManagerNoSnapshotFile(t *testing.T) {
	d := setupTestDB(t)
	sm := NewSnapshotManager(d, filepath.Join(t.TempDir(), "nonexistent", "vault.db"))

	err := sm.RestoreFromSnapshot()
	if err != nil {
		t.Fatalf("RestoreFromSnapshot with no file should return nil, got: %v", err)
	}
}

func TestSnapshotManagerLastSnapshot(t *testing.T) {
	d := setupTestDB(t)
	snapshotPath := filepath.Join(t.TempDir(), "snap.db")
	sm := NewSnapshotManager(d, snapshotPath)

	// Before any save, LastSnapshot should be zero.
	if !sm.LastSnapshot().IsZero() {
		t.Errorf("LastSnapshot before save should be zero, got %v", sm.LastSnapshot())
	}

	if err := sm.SaveSnapshot(); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// After save, LastSnapshot should be non-zero.
	if sm.LastSnapshot().IsZero() {
		t.Error("LastSnapshot after save should be non-zero")
	}
}

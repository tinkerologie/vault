package db

import (
	"path/filepath"
	"testing"
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

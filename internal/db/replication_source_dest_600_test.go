package db

import (
	"errors"
	"testing"
	"time"
)

// A replication source's replicated jobs follow its destination. Moving the
// source is refused while a job has restore points on the old destination,
// since their files stay there; jobs without restore points move with it.
func TestUpdateReplicationSourceDestination600(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	a, _ := d.CreateStorageDestination(StorageDestination{Name: "a", Type: "local", Config: "{}"})
	b, _ := d.CreateStorageDestination(StorageDestination{Name: "b", Type: "local", Config: "{}"})
	srcID, err := d.CreateReplicationSource(ReplicationSource{Name: "s", Type: "remote_vault", URL: "http://x", StorageDestID: a})
	if err != nil {
		t.Fatal(err)
	}
	withPoints, _ := d.CreateReplicatedJob(Job{Name: "[s] kept", SourceID: srcID, StorageDestID: a})
	empty, _ := d.CreateReplicatedJob(Job{Name: "[s] empty", SourceID: srcID, StorageDestID: a})
	if _, _, err := d.CreateReplicatedRestorePoint(
		JobRun{JobID: withPoints, Status: "success", BackupType: "full"},
		RestorePoint{JobID: withPoints, BackupType: "full", StoragePath: "J/r1", SourceID: srcID}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	src, err := d.GetReplicationSource(srcID)
	if err != nil {
		t.Fatal(err)
	}
	dests := func() (int64, int64, int64) {
		s, _ := d.GetReplicationSource(srcID)
		j1, _ := d.GetJob(withPoints)
		j2, _ := d.GetJob(empty)
		return s.StorageDestID, j1.StorageDestID, j2.StorageDestID
	}

	// Edits that keep the destination still work.
	src.Name = "renamed"
	if err := d.UpdateReplicationSource(src); err != nil {
		t.Fatalf("rename: %v", err)
	}

	for _, to := range []int64{b, 0} {
		moved := src
		moved.StorageDestID = to
		moved.Name = "moved"
		if err := d.UpdateReplicationSource(moved); !errors.Is(err, ErrReplicationDestinationInUse) {
			t.Fatalf("move to %d = %v, want ErrReplicationDestinationInUse", to, err)
		}
		if s, j1, j2 := dests(); s != a || j1 != a || j2 != a {
			t.Fatalf("refused move to %d changed destinations: source %d jobs %d %d", to, s, j1, j2)
		}
		if s, _ := d.GetReplicationSource(srcID); s.Name != "renamed" {
			t.Fatalf("refused move changed the name to %q", s.Name)
		}
	}

	// Without restore points, the jobs move with the source.
	if err := d.DeleteJob(withPoints); err != nil {
		t.Fatal(err)
	}
	src.StorageDestID = b
	if err := d.UpdateReplicationSource(src); err != nil {
		t.Fatalf("move with no restore points: %v", err)
	}
	if s, _ := d.GetReplicationSource(srcID); s.StorageDestID != b {
		t.Errorf("source destination = %d, want %d", s.StorageDestID, b)
	}
	if j, _ := d.GetJob(empty); j.StorageDestID != b {
		t.Errorf("job destination = %d, want %d", j.StorageDestID, b)
	}

	_ = d.Close()
	if err := d.UpdateReplicationSource(src); err == nil {
		t.Error("want an error from a closed database")
	}
}

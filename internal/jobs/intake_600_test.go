package jobs

import (
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

// seedReplicatedJob600 creates a replication source writing to a destination,
// and a replicated job of that source on the same destination.
func seedReplicatedJob600(t *testing.T, d *db.DB) (jobID, destA, destB int64) {
	t.Helper()
	destA = createLocalDest(t, d, "a", `{"path":"/mnt/a"}`)
	destB = createLocalDest(t, d, "b", `{"path":"/mnt/b"}`)
	srcID, err := d.CreateReplicationSource(db.ReplicationSource{
		Name: "peer", Type: "remote_vault", URL: "http://peer.invalid:24085", StorageDestID: destA, Config: "{}",
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	jobID, err = d.CreateReplicatedJob(db.Job{
		Name: "[peer] nightly", BackupTypeChain: "full", Compression: "zstd", Encryption: "none",
		NotifyOn: "failure", StorageDestID: destA, SourceID: srcID,
	})
	if err != nil {
		t.Fatalf("create replicated job: %v", err)
	}
	return jobID, destA, destB
}

// TestIntakeRefusesReplicatedDestinationChange600 pins that a replicated job's
// destination follows its source: restore and preflight open the job's
// destination, so moving it would point them at a place without the files.
func TestIntakeRefusesReplicatedDestinationChange600(t *testing.T) {
	for _, tc := range []struct {
		name string
		dest func(a, b int64) int64
	}{
		{"to another destination", func(_, b int64) int64 { return b }},
		{"to no destination", func(int64, int64) int64 { return 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, d, e := newIntake(t)
			jobID, a, b := seedReplicatedJob600(t, d)
			job, err := d.GetJob(jobID)
			if err != nil {
				t.Fatal(err)
			}
			job.StorageDestID = tc.dest(a, b)
			job.SourceID = 0 // the request's origin is ignored
			job.Name = "renamed"

			_, _, err = i.Update(jobID, job, nil)
			ve, ok := IsValidation(err)
			if !ok || ve.Field != "storage_dest_id" {
				t.Fatalf("Update = %v, want a storage_dest_id validation error", err)
			}
			after, err := d.GetJob(jobID)
			if err != nil {
				t.Fatal(err)
			}
			if after.StorageDestID != a || after.Name != "[peer] nightly" {
				t.Errorf("job changed after a refused update: dest %d name %q", after.StorageDestID, after.Name)
			}
			if e.reloads != 0 || e.flushes != 0 {
				t.Errorf("effects ran for a refused update: %d reloads, %d flushes", e.reloads, e.flushes)
			}
		})
	}
}

// TestIntakeAllowsOtherReplicatedEdits600 keeps the guard narrow: other edits
// of a replicated job, and destination edits of a local job, still work.
func TestIntakeAllowsOtherReplicatedEdits600(t *testing.T) {
	i, d, _ := newIntake(t)
	jobID, a, b := seedReplicatedJob600(t, d)
	job, err := d.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	job.RetentionCount = 9
	if _, _, err := i.Update(jobID, job, nil); err != nil {
		t.Fatalf("Update with unchanged destination: %v", err)
	}

	local := validJob()
	local.StorageDestID = a
	created, _, err := i.Create(local, nil)
	if err != nil {
		t.Fatal(err)
	}
	created.StorageDestID = b
	if _, _, err := i.Update(created.ID, created, nil); err != nil {
		t.Fatalf("Update of a local job's destination: %v", err)
	}
}

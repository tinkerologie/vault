package db

import "testing"

func TestListRestorePointOwners(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	here, err := d.CreateStorageDestination(StorageDestination{Name: "here", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	there, err := d.CreateStorageDestination(StorageDestination{Name: "there", Type: "local", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(job Job, path string) int64 {
		t.Helper()
		var id int64
		var err error
		if job.SourceID != 0 {
			id, err = d.CreateReplicatedJob(job)
		} else {
			id, err = d.CreateJob(job)
		}
		if err != nil {
			t.Fatal(err)
		}
		run, err := d.CreateJobRun(JobRun{JobID: id, Status: "completed", BackupType: "full"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.CreateRestorePoint(RestorePoint{JobRunID: run, JobID: id, BackupType: "full", StoragePath: path}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	self := mk(Job{Name: "self", BackupTypeChain: "full", StorageDestID: here}, "self/1")
	other := mk(Job{Name: "other", BackupTypeChain: "full", StorageDestID: here}, "other/1")
	mk(Job{Name: "elsewhere", BackupTypeChain: "full", StorageDestID: there}, "elsewhere/1")
	unbound := mk(Job{Name: "[S] J", SourceID: 7}, "J/1")
	// A local job whose destination was deleted, keeping its files, has no
	// destination either and may still hold data on this storage.
	orphan := mk(Job{Name: "orphan", BackupTypeChain: "full"}, "orphan/1")
	// A row with no path holds nothing.
	if _, err := d.Exec(`UPDATE restore_points SET storage_path = '' WHERE storage_path = 'self/1'`); err != nil {
		t.Fatal(err)
	}
	mk(Job{Name: "self2", BackupTypeChain: "full", StorageDestID: here}, "")

	owners, err := d.ListRestorePointOwners(here, self)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]RestorePointOwner{}
	for _, o := range owners {
		got[o.StoragePath] = o
	}
	if len(got) != 3 || got["other/1"].JobID != other || got["J/1"].JobID != unbound || got["J/1"].SourceID != 7 || got["orphan/1"].JobID != orphan {
		t.Fatalf("owners = %+v, want other/1 and the unbound J/1 and orphan/1", owners)
	}
	// The excluded job's own rows never appear.
	owners, err = d.ListRestorePointOwners(here, other)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range owners {
		if o.JobID == other {
			t.Fatalf("excluded job listed: %+v", o)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ListRestorePointOwners(here, 0); err == nil {
		t.Fatal("closed database returned no error")
	}
}

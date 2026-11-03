package db

import "testing"

func TestFirstJobRunStatus(t *testing.T) {
	t.Parallel()
	d := setupTestDB(t)
	jobID, err := d.CreateJob(Job{Name: "j", BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := d.FirstJobRunStatus(jobID); err != nil || s != "" {
		t.Fatalf("no runs = %q, %v", s, err)
	}
	for _, st := range []string{JobRunStatusImported, "completed"} {
		if _, err := d.CreateJobRun(JobRun{JobID: jobID, Status: st, BackupType: "full"}); err != nil {
			t.Fatal(err)
		}
	}
	if s, err := d.FirstJobRunStatus(jobID); err != nil || s != JobRunStatusImported {
		t.Fatalf("first = %q, %v", s, err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.FirstJobRunStatus(jobID); err == nil {
		t.Fatal("closed database returned no error")
	}
}

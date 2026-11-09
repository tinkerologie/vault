package db

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestAcknowledgeJobRunsPreservesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	jobID, err := d.CreateJob(Job{Name: "VMs", BackupTypeChain: "full"})
	if err != nil {
		t.Fatal(err)
	}
	completed := time.Now().UTC().Truncate(time.Second)
	var ids []int64
	for _, status := range []string{"failed", "error", "cancelled", "skipped", "partial", "running", "success"} {
		run := JobRun{JobID: jobID, Status: status, BackupType: "full", ItemsTotal: 5}
		run.ID, err = d.CreateJobRun(run)
		if err != nil {
			t.Fatal(err)
		}
		run.CompletedAt, run.ItemsDone, run.ItemsFailed, run.Log = &completed, 2, 3, "original output"
		if err := d.UpdateJobRun(run); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	before, err := d.GetJobRun(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if a, s, err := d.AcknowledgeJobRuns(ctx, nil, "api", ""); a != 0 || s != 0 || err != nil {
		t.Fatalf("empty request: %d acknowledged, %d skipped, error %v", a, s, err)
	}
	if a, s, err := d.AcknowledgeJobRuns(ctx, append(ids, ids[0], 999), "api", "Reviewed"); a != 5 || s != 3 || err != nil {
		t.Fatalf("first request: %d acknowledged, %d skipped, error %v", a, s, err)
	}
	if a, s, err := d.AcknowledgeJobRuns(ctx, ids[:5], "other", "Overwrite"); a != 0 || s != 5 || err != nil {
		t.Fatalf("retry: %d acknowledged, %d skipped, error %v", a, s, err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	after, err := reopened.GetJobRun(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if after.AcknowledgedAt == nil || after.AcknowledgedBy == nil || *after.AcknowledgedBy != "api" ||
		after.AcknowledgeReason == nil || *after.AcknowledgeReason != "Reviewed" {
		t.Fatalf("acknowledgement was not preserved: %+v", after)
	}
	after.AcknowledgedAt, after.AcknowledgedBy, after.AcknowledgeReason = nil, nil, nil
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("outcome changed: before %+v, after %+v", before, after)
	}
	runs, err := reopened.ListJobRuns(ctx, 10)
	if err != nil || len(runs) != len(ids) {
		t.Fatalf("read history: %v, %v", runs, err)
	}
	for _, run := range runs {
		want := run.ID <= ids[4]
		if got := run.AcknowledgedAt != nil; got != want {
			t.Errorf("run %d (%s): acknowledged %t, want %t", run.ID, run.Status, got, want)
		}
	}
}

func TestLegacyRunAcknowledgementMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO jobs (id, name) VALUES (1, 'Legacy job');
		INSERT INTO job_runs (id, job_id, status, backup_type) VALUES (1, 1, 'cancelled', 'full')`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		d, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		run, err := d.GetJobRun(1)
		closeErr := d.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read error %v, close error %v", err, closeErr)
		}
		if run.Status != "cancelled" || run.AcknowledgedAt != nil || run.AcknowledgedBy != nil || run.AcknowledgeReason != nil {
			t.Fatalf("migration changed a legacy run: %+v", run)
		}
	}
}

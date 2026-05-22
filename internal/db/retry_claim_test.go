package db

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestClaimDueRetriesIsAtomic(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vault.db")
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	// Insert a job + storage destination so the FK is satisfied.
	if _, err := d.Exec(
		`INSERT INTO storage_destinations (name, type, config) VALUES ('t', 'local', '{}')`,
	); err != nil {
		t.Fatalf("storage insert: %v", err)
	}
	res, err := d.Exec(
		`INSERT INTO jobs (name, storage_dest_id) VALUES ('j', 1)`,
	)
	if err != nil {
		t.Fatalf("job insert: %v", err)
	}
	jobID, _ := res.LastInsertId()

	// Insert a failed run with retry_next_at in the past.
	res, err = d.Exec(
		`INSERT INTO job_runs (job_id, status, backup_type, retry_next_at)
		 VALUES (?, 'failed', 'full', datetime('now', '-1 minute'))`,
		jobID,
	)
	if err != nil {
		t.Fatalf("run insert: %v", err)
	}
	runID, _ := res.LastInsertId()

	// Two concurrent claim attempts — exactly one should claim the row.
	var c1, c2 []DueRetry
	var e1, e2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c1, e1 = d.ClaimDueRetries() }()
	go func() { defer wg.Done(); c2, e2 = d.ClaimDueRetries() }()
	wg.Wait()

	if e1 != nil || e2 != nil {
		t.Fatalf("errors: %v %v", e1, e2)
	}
	total := len(c1) + len(c2)
	if total != 1 {
		t.Errorf("expected exactly 1 total claim across two callers, got %d (c1=%v c2=%v)", total, c1, c2)
	}
	if total == 1 {
		var found DueRetry
		if len(c1) == 1 {
			found = c1[0]
		} else {
			found = c2[0]
		}
		if found.OriginalRunID != runID || found.JobID != jobID {
			t.Errorf("wrong row claimed: got %+v want runID=%d jobID=%d", found, runID, jobID)
		}
	}

	// After claim, retry_next_at should be NULL.
	var hasRetry int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM job_runs WHERE id = ? AND retry_next_at IS NOT NULL`,
		runID,
	).Scan(&hasRetry); err != nil {
		t.Fatalf("post-claim query: %v", err)
	}
	if hasRetry != 0 {
		t.Errorf("retry_next_at not cleared after claim")
	}

	// Second call returns nothing.
	again, err := d.ClaimDueRetries()
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second claim returned %d rows, want 0", len(again))
	}
}

func TestClaimDueRetriesIgnoresFutureAndNonFailed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vault.db")
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if _, err := d.Exec(
		`INSERT INTO storage_destinations (name, type, config) VALUES ('t', 'local', '{}')`,
	); err != nil {
		t.Fatalf("storage insert: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO jobs (name, storage_dest_id) VALUES ('j', 1)`,
	); err != nil {
		t.Fatalf("job insert: %v", err)
	}

	// Future retry — should NOT be claimed.
	if _, err := d.Exec(
		`INSERT INTO job_runs (job_id, status, backup_type, retry_next_at)
		 VALUES (1, 'failed', 'full', datetime('now', '+1 hour'))`,
	); err != nil {
		t.Fatalf("future insert: %v", err)
	}
	// Success row with retry_next_at in past (shouldn't happen IRL but defensive) — NOT claimed.
	if _, err := d.Exec(
		`INSERT INTO job_runs (job_id, status, backup_type, retry_next_at)
		 VALUES (1, 'success', 'full', datetime('now', '-1 hour'))`,
	); err != nil {
		t.Fatalf("success insert: %v", err)
	}

	claimed, err := d.ClaimDueRetries()
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("expected 0 claims, got %d", len(claimed))
	}
}

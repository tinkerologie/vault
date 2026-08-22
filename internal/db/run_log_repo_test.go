package db

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// openRunLogTestDB opens a fresh database on a temp dir. The schema const
// creates run_log_entries on open, so no extra setup is needed.
func openRunLogTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedRun inserts one job and one run row and returns the run ID.
func seedRun(t *testing.T, d *DB, name string) int64 {
	t.Helper()
	jobID, err := d.CreateJob(Job{Name: name})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	runID, err := d.CreateJobRun(JobRun{JobID: jobID, Status: "running", BackupType: "full"})
	if err != nil {
		t.Fatalf("create job run: %v", err)
	}
	return runID
}

func TestListRunLogEntries(t *testing.T) {
	tests := []struct {
		name    string
		inserts []RunLogEntry
		afterID int64
		limit   int
		wantIDs []int64
	}{
		{
			name: "returns entries oldest first",
			inserts: []RunLogEntry{
				{Level: "info", Message: "first"},
				{Level: "info", Message: "second"},
			},
			limit:   100,
			wantIDs: []int64{1, 2},
		},
		{
			name: "after id tails only newer entries",
			inserts: []RunLogEntry{
				{Level: "info", Message: "first"},
				{Level: "info", Message: "second"},
				{Level: "info", Message: "third"},
			},
			afterID: 2,
			limit:   100,
			wantIDs: []int64{3},
		},
		{
			name: "limit below one is clamped to one",
			inserts: []RunLogEntry{
				{Level: "info", Message: "first"},
				{Level: "info", Message: "second"},
			},
			limit:   0,
			wantIDs: []int64{1},
		},
		{
			name:    "run without entries yields nil slice",
			inserts: nil,
			wantIDs: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := openRunLogTestDB(t)
			runID := seedRun(t, d, "runlog-"+tt.name)
			for _, ins := range tt.inserts {
				ins.RunID = runID
				if _, err := d.AppendRunLog(context.Background(), ins); err != nil {
					t.Fatalf("append run log: %v", err)
				}
			}
			got, err := d.ListRunLogEntries(context.Background(), runID, tt.afterID, tt.limit)
			if err != nil {
				t.Fatalf("list run log: %v", err)
			}
			var gotIDs []int64
			for _, e := range got {
				gotIDs = append(gotIDs, e.ID)
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("got ids %v, want %v", gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Fatalf("got ids %v, want %v", gotIDs, tt.wantIDs)
				}
			}
		})
	}
}

func TestTailRunLogEntries(t *testing.T) {
	tests := []struct {
		name    string
		inserts []RunLogEntry
		limit   int
		wantIDs []int64
	}{
		{
			name: "returns the newest lines oldest-first within the window",
			inserts: []RunLogEntry{
				{Level: "info", Message: "first"},
				{Level: "info", Message: "second"},
				{Level: "info", Message: "third"},
			},
			limit:   2,
			wantIDs: []int64{2, 3},
		},
		{
			name: "limit above entry count returns the whole log",
			inserts: []RunLogEntry{
				{Level: "info", Message: "first"},
				{Level: "info", Message: "second"},
			},
			limit:   100,
			wantIDs: []int64{1, 2},
		},
		{
			name: "limit below one is clamped to one",
			inserts: []RunLogEntry{
				{Level: "info", Message: "first"},
				{Level: "info", Message: "second"},
			},
			limit:   0,
			wantIDs: []int64{2},
		},
		{
			name:    "run without entries yields nil slice",
			inserts: nil,
			limit:   100,
			wantIDs: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := openRunLogTestDB(t)
			runID := seedRun(t, d, "runlog-tail-"+tt.name)
			for _, ins := range tt.inserts {
				ins.RunID = runID
				if _, err := d.AppendRunLog(context.Background(), ins); err != nil {
					t.Fatalf("append run log: %v", err)
				}
			}
			got, err := d.TailRunLogEntries(context.Background(), runID, tt.limit)
			if err != nil {
				t.Fatalf("tail run log: %v", err)
			}
			var gotIDs []int64
			for _, e := range got {
				gotIDs = append(gotIDs, e.ID)
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("got ids %v, want %v", gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Fatalf("got ids %v, want %v", gotIDs, tt.wantIDs)
				}
			}
		})
	}
}

func TestAppendRunLogStoresData(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		wantData string
	}{
		{name: "json data round-trips", data: `{"k":"v"}`, wantData: `{"k":"v"}`},
		{name: "empty data stays empty", data: "", wantData: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := openRunLogTestDB(t)
			runID := seedRun(t, d, "runlog-data-"+tt.name)
			if _, err := d.AppendRunLog(context.Background(), RunLogEntry{RunID: runID, Level: "info", Message: "m", Data: tt.data}); err != nil {
				t.Fatalf("append: %v", err)
			}
			got, err := d.ListRunLogEntries(context.Background(), runID, 0, 100)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d entries, want 1", len(got))
			}
			if got[0].Data != tt.wantData {
				t.Fatalf("data = %q, want %q", got[0].Data, tt.wantData)
			}
			if got[0].RunID != runID {
				t.Fatalf("run_id = %d, want %d", got[0].RunID, runID)
			}
		})
	}
}

func TestRunLogEntriesCascadeWithJob(t *testing.T) {
	t.Parallel()
	d := openRunLogTestDB(t)
	runID := seedRun(t, d, "runlog-cascade")
	if _, err := d.AppendRunLog(context.Background(), RunLogEntry{RunID: runID, Level: "info", Message: "doomed"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	run, err := d.GetJobRun(runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if err := d.DeleteJob(run.JobID); err != nil {
		t.Fatalf("delete job: %v", err)
	}

	// Single-scenario integration test: run_log_entries carries an ON DELETE
	// CASCADE on the run row, so deleting the job must delete the entries.
	cases := []struct {
		name string
	}{
		{name: "deleting the job cascades to the run's log entries"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.ListRunLogEntries(context.Background(), runID, 0, 100)
			if err != nil {
				t.Fatalf("list after delete: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("expected cascade-deleted entries, got %d", len(got))
			}
		})
	}
}

func TestCapRunLogEntriesPerRun(t *testing.T) {
	tests := []struct {
		name        string
		primary     int
		second      int
		maxLines    int
		wantPrimary int
		wantSecond  int
	}{
		{name: "run under the cap keeps all lines", primary: 3, second: 0, maxLines: 5, wantPrimary: 3, wantSecond: 0},
		{name: "run over the cap keeps only the newest lines", primary: 5, second: 0, maxLines: 2, wantPrimary: 2, wantSecond: 0},
		{name: "cap of one keeps the latest line", primary: 4, second: 0, maxLines: 1, wantPrimary: 1, wantSecond: 0},
		{name: "runs are capped independently", primary: 6, second: 3, maxLines: 2, wantPrimary: 2, wantSecond: 2},
		{name: "zero max lines deletes every line", primary: 2, second: 0, maxLines: 0, wantPrimary: 0, wantSecond: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := openRunLogTestDB(t)
			runID := seedRun(t, d, "runlog-cap-"+tt.name)
			var otherID int64
			if tt.second > 0 {
				otherID = seedRun(t, d, "runlog-cap-other-"+tt.name)
			}
			for i := 0; i < tt.primary; i++ {
				if _, err := d.AppendRunLog(context.Background(), RunLogEntry{RunID: runID, Level: "info", Message: fmt.Sprintf("line-%d", i)}); err != nil {
					t.Fatalf("append primary: %v", err)
				}
			}
			for i := 0; i < tt.second; i++ {
				if _, err := d.AppendRunLog(context.Background(), RunLogEntry{RunID: otherID, Level: "info", Message: fmt.Sprintf("other-%d", i)}); err != nil {
					t.Fatalf("append second: %v", err)
				}
			}
			if err := d.CapRunLogEntriesPerRun(context.Background(), tt.maxLines); err != nil {
				t.Fatalf("cap: %v", err)
			}
			got, err := d.ListRunLogEntries(context.Background(), runID, 0, 1000)
			if err != nil {
				t.Fatalf("list primary: %v", err)
			}
			if len(got) != tt.wantPrimary {
				t.Fatalf("primary run: got %d entries, want %d", len(got), tt.wantPrimary)
			}
			if tt.wantPrimary > 0 {
				wantFirst := fmt.Sprintf("line-%d", tt.primary-tt.wantPrimary)
				if got[0].Message != wantFirst {
					t.Fatalf("oldest survivor = %q, want %q (newest N must survive)", got[0].Message, wantFirst)
				}
			}
			if tt.second > 0 {
				other, err := d.ListRunLogEntries(context.Background(), otherID, 0, 1000)
				if err != nil {
					t.Fatalf("list second: %v", err)
				}
				if len(other) != tt.wantSecond {
					t.Fatalf("second run: got %d entries, want %d", len(other), tt.wantSecond)
				}
			}
		})
	}
}

func TestAppendRunLogEnforcesPerRunCap(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "cap enforced after seeding at limit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := openRunLogTestDB(t)
			ctx := context.Background()
			runID := seedRun(t, d, "runlog-live-cap")
			otherID := seedRun(t, d, "runlog-live-cap-other")

			// Seed exactly cap rows directly in one transaction — paying for 10k
			// individual AppendRunLog calls would slow the test for no gain.
			tx, err := d.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin seed tx: %v", err)
			}
			stmt, err := tx.PrepareContext(ctx, `INSERT INTO run_log_entries (run_id, level, message) VALUES (?, 'info', ?)`)
			if err != nil {
				t.Fatalf("prepare seed: %v", err)
			}
			for i := 0; i < MaxRunLogEntriesPerRun; i++ {
				if _, err := stmt.ExecContext(ctx, runID, fmt.Sprintf("seed-%d", i)); err != nil {
					t.Fatalf("seed row %d: %v", i, err)
				}
			}
			if err := stmt.Close(); err != nil {
				t.Fatalf("close seed stmt: %v", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO run_log_entries (run_id, level, message) VALUES (?, 'info', 'untouched')`, otherID); err != nil {
				t.Fatalf("seed other run: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit seed: %v", err)
			}

			// One append through the public API must trim the run back to the cap:
			// the oldest seed goes, the new entry survives, the other run is untouched.
			id, err := d.AppendRunLog(ctx, RunLogEntry{RunID: runID, Level: "info", Message: "newest"})
			if err != nil {
				t.Fatalf("append: %v", err)
			}

			var n int
			if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_log_entries WHERE run_id = ?`, runID).Scan(&n); err != nil {
				t.Fatalf("count: %v", err)
			}
			if n != MaxRunLogEntriesPerRun {
				t.Fatalf("entries = %d, want cap %d", n, MaxRunLogEntriesPerRun)
			}
			var oldest string
			if err := d.QueryRowContext(ctx, `SELECT message FROM run_log_entries WHERE run_id = ? ORDER BY id ASC LIMIT 1`, runID).Scan(&oldest); err != nil {
				t.Fatalf("oldest: %v", err)
			}
			if oldest != "seed-1" {
				t.Fatalf("oldest survivor = %q, want %q (seed-0 must be evicted)", oldest, "seed-1")
			}
			var newest string
			if err := d.QueryRowContext(ctx, `SELECT message FROM run_log_entries WHERE id = ?`, id).Scan(&newest); err != nil {
				t.Fatalf("newest: %v", err)
			}
			if newest != "newest" {
				t.Fatalf("appended entry message = %q, want %q", newest, "newest")
			}
			var otherN int
			if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_log_entries WHERE run_id = ?`, otherID).Scan(&otherN); err != nil {
				t.Fatalf("count other: %v", err)
			}
			if otherN != 1 {
				t.Fatalf("other run entries = %d, want 1 (prune must be run-scoped)", otherN)
			}
		})
	}
}

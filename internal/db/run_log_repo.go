package db

import (
	"context"
	"fmt"
)

// MaxRunLogEntriesPerRun is the per-run entry cap AppendRunLog enforces
// on every insert. Normal retention is the ON DELETE CASCADE with the run
// row; this bound stops a pathological run (e.g. a very verbose pre/post
// script) from growing the table without limit before that cascade applies.
const MaxRunLogEntriesPerRun = 10000

// AppendRunLog inserts one run log entry and returns its row ID. The
// insert and the run-scoped prune back to MaxRunLogEntriesPerRun happen in
// one transaction, so the cap holds while a run is still writing — not
// just at the next process start. Callers treat errors as non-fatal (a
// logging failure must never abort a run), matching the fire-and-forget
// style of LogActivity. The runner passes a detached context (see
// Runner.runLog): terminal entries such as the end-of-run summary are
// written exactly when the run's own context is being cancelled, so the
// append must not inherit that cancellation.
func (d *DB) AppendRunLog(ctx context.Context, entry RunLogEntry) (int64, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("append run log for run %d: begin tx: %w", entry.RunID, err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a successful Commit is a no-op
	res, err := tx.ExecContext(ctx,
		`INSERT INTO run_log_entries (run_id, level, message, data) VALUES (?, ?, ?, ?)`,
		entry.RunID, entry.Level, entry.Message, entry.Data,
	)
	if err != nil {
		return 0, fmt.Errorf("append run log for run %d: %w", entry.RunID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("append run log for run %d: last insert id: %w", entry.RunID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM run_log_entries WHERE run_id = ? AND id NOT IN (
			SELECT id FROM run_log_entries WHERE run_id = ? ORDER BY id DESC LIMIT ?
		)`, entry.RunID, entry.RunID, MaxRunLogEntriesPerRun,
	); err != nil {
		return 0, fmt.Errorf("prune run log entries for run %d: %w", entry.RunID, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("append run log for run %d: commit: %w", entry.RunID, err)
	}
	return id, nil
}

// ListRunLogEntries returns a run's log entries oldest-first. afterID
// supports tailing: only entries with id > afterID are returned. limit
// is clamped to [1, 1000].
func (d *DB) ListRunLogEntries(ctx context.Context, runID int64, afterID int64, limit int) ([]RunLogEntry, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := d.QueryContext(ctx,
		`SELECT id, run_id, ts, level, message, data
		FROM run_log_entries
		WHERE run_id = ? AND id > ?
		ORDER BY id ASC
		LIMIT ?`,
		runID, afterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list run log entries for run %d: %w", runID, err)
	}
	defer rows.Close()
	var entries []RunLogEntry
	for rows.Next() {
		var e RunLogEntry
		if err := rows.Scan(&e.ID, &e.RunID, &e.Ts, &e.Level, &e.Message, &e.Data); err != nil {
			return nil, fmt.Errorf("scan run log entry for run %d: %w", runID, err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run log entries for run %d: %w", runID, err)
	}
	return entries, nil
}

// TailRunLogEntries returns the LAST `limit` entries of a run's log,
// oldest-first within that window. The unified console fetches the tail of
// every terminal run: the head-first List path returns the oldest lines
// first, so for a run with more lines than `limit` the end-of-run summary
// (the newest line — the status/size/duration the console must show) would
// never be fetched and the run would appear stuck mid-flight. limit is
// clamped to [1, 1000] like ListRunLogEntries.
func (d *DB) TailRunLogEntries(ctx context.Context, runID int64, limit int) ([]RunLogEntry, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	// Inner query takes the newest `limit` rows; the outer query re-sorts
	// them oldest-first to match ListRunLogEntries' ordering contract.
	rows, err := d.QueryContext(ctx,
		`SELECT id, run_id, ts, level, message, data FROM (
			SELECT id, run_id, ts, level, message, data
			FROM run_log_entries
			WHERE run_id = ?
			ORDER BY id DESC
			LIMIT ?
		) ORDER BY id ASC`,
		runID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("tail run log entries for run %d: %w", runID, err)
	}
	defer rows.Close()
	var entries []RunLogEntry
	for rows.Next() {
		var e RunLogEntry
		if err := rows.Scan(&e.ID, &e.RunID, &e.Ts, &e.Level, &e.Message, &e.Data); err != nil {
			return nil, fmt.Errorf("scan run log entry for run %d: %w", runID, err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run log entries for run %d: %w", runID, err)
	}
	return entries, nil
}

// CapRunLogEntriesPerRun ensures no single run exceeds maxLines log
// entries, deleting the older lines first. AppendRunLog already enforces
// MaxRunLogEntriesPerRun on every insert, so this runs at process start
// purely as recovery cleanup for rows written before the live cap existed.
func (d *DB) CapRunLogEntriesPerRun(ctx context.Context, maxLines int) error {
	if _, err := d.ExecContext(ctx,
		`DELETE FROM run_log_entries WHERE id NOT IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY run_id ORDER BY id DESC) AS rn
				FROM run_log_entries
			) WHERE rn <= ?
		)`, maxLines,
	); err != nil {
		return fmt.Errorf("cap run log entries per run at %d: %w", maxLines, err)
	}
	return nil
}

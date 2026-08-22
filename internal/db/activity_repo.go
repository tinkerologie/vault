package db

import "fmt"

// MaxActivityLogRows is the total-row cap CreateActivityLog enforces on
// every insert (10k, matching the daemon's startup CapActivityLogs call).
// Enforcing it live — not only at startup — keeps the table bounded on
// long-running daemons: per-container health entries alone can add dozens
// of rows per backup run, so the startup-only cap could be exceeded within
// weeks of continuous uptime.
const MaxActivityLogRows = 10000

// CreateActivityLog inserts a new activity log entry and returns its row ID.
// The insert and the prune back to MaxActivityLogRows happen in one
// transaction, mirroring AppendRunLog, so the cap holds while the daemon
// keeps writing rather than only at the next process start.
func (d *DB) CreateActivityLog(entry ActivityLogEntry) (int64, error) {
	tx, err := d.Begin()
	if err != nil {
		return 0, fmt.Errorf("create activity log: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a successful Commit is a no-op
	res, err := tx.Exec(
		"INSERT INTO activity_log (level, category, message, details) VALUES (?, ?, ?, ?)",
		entry.Level, entry.Category, entry.Message, entry.Details,
	)
	if err != nil {
		return 0, fmt.Errorf("create activity log: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create activity log: last insert id: %w", err)
	}
	if _, err := tx.Exec(
		`DELETE FROM activity_log WHERE id NOT IN (
			SELECT id FROM activity_log ORDER BY created_at DESC, id DESC LIMIT ?
		)`, MaxActivityLogRows,
	); err != nil {
		return 0, fmt.Errorf("prune activity log: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("create activity log: commit: %w", err)
	}
	return id, nil
}

// ListActivityLogs returns recent activity log entries with optional
// filtering. beforeID, when > 0, pages backwards: only entries with a
// smaller id are returned. Ids are assigned in insertion order, so the
// id cursor composes cleanly with the created_at DESC ordering.
func (d *DB) ListActivityLogs(limit int, category string, beforeID int64) ([]ActivityLogEntry, error) {
	conds := "1=1"
	var args []any

	if category != "" {
		conds += " AND category = ?"
		args = append(args, category)
	}
	if beforeID > 0 {
		conds += " AND id < ?"
		args = append(args, beforeID)
	}
	query := `SELECT id, level, category, message, details, created_at
		FROM activity_log WHERE ` + conds + `
		ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ActivityLogEntry
	for rows.Next() {
		var e ActivityLogEntry
		if err := rows.Scan(&e.ID, &e.Level, &e.Category, &e.Message, &e.Details, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// DeleteOldActivityLogs removes entries older than the specified number of days.
func (d *DB) DeleteOldActivityLogs(keepDays int) error {
	_, err := d.Exec(
		"DELETE FROM activity_log WHERE created_at < datetime('now', '-' || ? || ' days')",
		keepDays,
	)
	return err
}

// CapActivityLogs ensures the activity log doesn't exceed maxRows entries.
func (d *DB) CapActivityLogs(maxRows int) error {
	_, err := d.Exec(
		`DELETE FROM activity_log WHERE id NOT IN (
			SELECT id FROM activity_log ORDER BY created_at DESC, id DESC LIMIT ?
		)`, maxRows,
	)
	return err
}

// LogActivity is a convenience method for inserting a log entry.
func (d *DB) LogActivity(level, category, message, details string) {
	_, _ = d.CreateActivityLog(ActivityLogEntry{
		Level:    level,
		Category: category,
		Message:  message,
		Details:  details,
	})
}

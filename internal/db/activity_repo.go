package db

// CreateActivityLog inserts a new activity log entry.
func (d *DB) CreateActivityLog(entry ActivityLogEntry) (int64, error) {
	res, err := d.Exec(
		"INSERT INTO activity_log (level, category, message, details) VALUES (?, ?, ?, ?)",
		entry.Level, entry.Category, entry.Message, entry.Details,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListActivityLogs returns recent activity log entries with optional filtering.
func (d *DB) ListActivityLogs(limit int, category string) ([]ActivityLogEntry, error) {
	var query string
	var args []any

	if category != "" {
		query = `SELECT id, level, category, message, details, created_at
			FROM activity_log WHERE category = ? ORDER BY created_at DESC LIMIT ?`
		args = []any{category, limit}
	} else {
		query = `SELECT id, level, category, message, details, created_at
			FROM activity_log ORDER BY created_at DESC LIMIT ?`
		args = []any{limit}
	}

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

// LogActivity is a convenience method for inserting a log entry.
func (d *DB) LogActivity(level, category, message, details string) {
	_, _ = d.CreateActivityLog(ActivityLogEntry{
		Level:    level,
		Category: category,
		Message:  message,
		Details:  details,
	})
}

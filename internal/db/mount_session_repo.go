package db

import (
	"database/sql"
	"fmt"
	"time"
)

// CreateMountSession inserts a new mount session and returns its ID.
func (d *DB) CreateMountSession(s MountSession) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO mount_sessions (job_id, restore_point_id, storage_dest_id, mount_path, status, error)
		VALUES (?, ?, ?, ?, ?, ?)`,
		s.JobID, s.RestorePointID, s.StorageDestID, s.MountPath, "active", s.Error,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetMountSession returns a single mount session by ID, including joined job and storage names.
func (d *DB) GetMountSession(id int64) (MountSession, error) {
	var s MountSession
	var rpID sql.NullInt64
	err := d.QueryRow(
		`SELECT m.id, m.job_id, COALESCE(j.name, ''), m.restore_point_id, m.storage_dest_id,
			COALESCE(sd.name, ''), m.mount_path, m.status, m.started_at, m.stopped_at,
			m.last_activity_at, m.error
		FROM mount_sessions m
		LEFT JOIN jobs j ON j.id = m.job_id
		LEFT JOIN storage_destinations sd ON sd.id = m.storage_dest_id
		WHERE m.id = ?`, id,
	).Scan(
		&s.ID, &s.JobID, &s.JobName, &rpID, &s.StorageDestID,
		&s.StorageName, &s.MountPath, &s.Status, &s.StartedAt, &s.StoppedAt,
		&s.LastActivityAt, &s.Error,
	)
	if err == sql.ErrNoRows {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	if rpID.Valid {
		v := rpID.Int64
		s.RestorePointID = &v
	}
	return s, nil
}

// ListMountSessions returns mount sessions, optionally filtered to active only.
func (d *DB) ListMountSessions(activeOnly bool) ([]MountSession, error) {
	query := `SELECT m.id, m.job_id, COALESCE(j.name, ''), m.restore_point_id, m.storage_dest_id,
			COALESCE(sd.name, ''), m.mount_path, m.status, m.started_at, m.stopped_at,
			m.last_activity_at, m.error
		FROM mount_sessions m
		LEFT JOIN jobs j ON j.id = m.job_id
		LEFT JOIN storage_destinations sd ON sd.id = m.storage_dest_id`
	if activeOnly {
		query += ` WHERE m.status = 'active'`
	}
	query += ` ORDER BY m.started_at DESC, m.id DESC`

	rows, err := d.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MountSession
	for rows.Next() {
		var s MountSession
		var rpID sql.NullInt64
		if err := rows.Scan(
			&s.ID, &s.JobID, &s.JobName, &rpID, &s.StorageDestID,
			&s.StorageName, &s.MountPath, &s.Status, &s.StartedAt, &s.StoppedAt,
			&s.LastActivityAt, &s.Error,
		); err != nil {
			return nil, err
		}
		if rpID.Valid {
			v := rpID.Int64
			s.RestorePointID = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateMountSessionStatus updates status and error. If not active, sets stopped_at to now.
func (d *DB) UpdateMountSessionStatus(id int64, status string, errStr string) error {
	if status == "active" {
		_, err := d.Exec(
			`UPDATE mount_sessions SET status = ?, error = ? WHERE id = ?`,
			status, errStr, id,
		)
		return err
	}
	now := time.Now().UTC()
	_, err := d.Exec(
		`UPDATE mount_sessions SET status = ?, stopped_at = ?, error = ? WHERE id = ?`,
		status, now, errStr, id,
	)
	return err
}

// UpdateMountSessionActivity sets last_activity_at to current timestamp.
func (d *DB) UpdateMountSessionActivity(id int64) error {
	now := time.Now().UTC()
	_, err := d.Exec(`UPDATE mount_sessions SET last_activity_at = ? WHERE id = ?`, now, id)
	return err
}

// UpdateMountSessionPath updates the mount directory path for a session.
func (d *DB) UpdateMountSessionPath(id int64, mountPath string) error {
	_, err := d.Exec(`UPDATE mount_sessions SET mount_path = ? WHERE id = ?`, mountPath, id)
	return err
}

// CleanupStaleMountSessions finds all active sessions, marks them crashed, and returns them for unmounting.
func (d *DB) CleanupStaleMountSessions() ([]MountSession, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, fmt.Errorf("cleanup stale mount sessions: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after a successful Commit is a no-op

	query := `SELECT m.id, m.job_id, COALESCE(j.name, ''), m.restore_point_id, m.storage_dest_id,
			COALESCE(sd.name, ''), m.mount_path, m.status, m.started_at, m.stopped_at,
			m.last_activity_at, m.error
		FROM mount_sessions m
		LEFT JOIN jobs j ON j.id = m.job_id
		LEFT JOIN storage_destinations sd ON sd.id = m.storage_dest_id
		WHERE m.status = 'active'
		ORDER BY m.started_at DESC, m.id DESC`

	rows, err := tx.Query(query)
	if err != nil {
		return nil, fmt.Errorf("cleanup stale mount sessions: query active: %w", err)
	}
	defer rows.Close()

	var active []MountSession
	for rows.Next() {
		var s MountSession
		var rpID sql.NullInt64
		if err := rows.Scan(
			&s.ID, &s.JobID, &s.JobName, &rpID, &s.StorageDestID,
			&s.StorageName, &s.MountPath, &s.Status, &s.StartedAt, &s.StoppedAt,
			&s.LastActivityAt, &s.Error,
		); err != nil {
			return nil, fmt.Errorf("cleanup stale mount sessions: scan: %w", err)
		}
		if rpID.Valid {
			v := rpID.Int64
			s.RestorePointID = &v
		}
		active = append(active, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cleanup stale mount sessions: rows: %w", err)
	}
	rows.Close()

	if len(active) == 0 {
		return nil, nil
	}

	now := time.Now().UTC()
	_, err = tx.Exec(`UPDATE mount_sessions SET status = 'crashed', stopped_at = ?, error = 'daemon restarted while mount was active' WHERE status = 'active'`, now)
	if err != nil {
		return nil, fmt.Errorf("cleanup stale mount sessions: update status: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("cleanup stale mount sessions: commit tx: %w", err)
	}
	return active, nil
}

// DeleteMountSession deletes a mount session record.
func (d *DB) DeleteMountSession(id int64) error {
	_, err := d.Exec(`DELETE FROM mount_sessions WHERE id = ?`, id)
	return err
}

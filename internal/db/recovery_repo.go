package db

import "context"

// RestorePointMembershipPage reads a bounded page across Jobs in descending ID
// order. Storage paths and Destination settings are unnecessary for discovery.
func (d *DB) RestorePointMembershipPage(ctx context.Context, beforeID int64, limit int) ([]RestorePoint, error) {
	query := `SELECT id, job_id, backup_type, created_at, metadata FROM restore_points`
	args := []any{}
	if beforeID > 0 {
		query += " WHERE id < ?"
		args = append(args, beforeID)
	}
	args = append(args, limit)
	rows, err := d.QueryContext(ctx, query+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make([]RestorePoint, 0, limit)
	for rows.Next() {
		var point RestorePoint
		if err := rows.Scan(&point.ID, &point.JobID, &point.BackupType, &point.CreatedAt, &point.Metadata); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

// RestorePointChainLinks follows only ancestors of the catalog's current page.
// UNION deduplicates shared links and terminates cycles. The CTE limit bounds
// rows admitted to the recursive queue even for very long chains.
func (d *DB) RestorePointChainLinks(ctx context.Context, jobID int64, pageSize, linkLimit int) (map[int64]RestorePoint, bool, error) {
	rows, err := d.QueryContext(ctx, `
		WITH RECURSIVE selected(id, backup_type, parent_id) AS (
			SELECT id, backup_type, COALESCE(parent_restore_point_id, 0)
			FROM restore_points WHERE job_id = ?
			ORDER BY created_at DESC, id DESC LIMIT ?
		),
		links(id, backup_type, parent_id) AS (
			SELECT id, backup_type, parent_id FROM selected
			UNION
			SELECT p.id, p.backup_type, COALESCE(p.parent_restore_point_id, 0)
			FROM links l JOIN restore_points p ON p.id = l.parent_id
			WHERE p.job_id = ?
			LIMIT ?
		)
		SELECT id, backup_type, parent_id FROM links`, jobID, pageSize, jobID, linkLimit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	links := make(map[int64]RestorePoint)
	for rows.Next() {
		var point RestorePoint
		if err := rows.Scan(&point.ID, &point.BackupType, &point.ParentRestorePointID); err != nil {
			return nil, false, err
		}
		links[point.ID] = point
	}
	return links, len(links) > linkLimit, rows.Err()
}

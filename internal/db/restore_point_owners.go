package db

// RestorePointOwner names the job holding a restore point's storage path.
type RestorePointOwner struct {
	JobID       int64
	SourceID    int64
	StoragePath string
}

// ListRestorePointOwners returns the storage paths of restore points that jobs
// other than excludeJobID hold on destID. A job with no destination recorded
// counts on every one: a replicated job from a source with no fixed
// destination, or a local job whose destination was deleted with its files kept,
// may still hold data on this storage. Over-counting only refuses more.
func (d *DB) ListRestorePointOwners(destID, excludeJobID int64) ([]RestorePointOwner, error) {
	rows, err := d.Query(`
		SELECT rp.job_id, COALESCE(j.source_id, 0), rp.storage_path
		  FROM restore_points rp
		  JOIN jobs j ON j.id = rp.job_id
		 WHERE rp.job_id <> ? AND rp.storage_path <> ''
		   AND (j.storage_dest_id = ? OR j.storage_dest_id IS NULL)`,
		excludeJobID, destID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owners []RestorePointOwner
	for rows.Next() {
		var o RestorePointOwner
		if err := rows.Scan(&o.JobID, &o.SourceID, &o.StoragePath); err != nil {
			return nil, err
		}
		owners = append(owners, o)
	}
	return owners, rows.Err()
}

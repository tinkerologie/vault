package db

import "database/sql"

func (d *DB) CreateStorageDestination(dest StorageDestination) (int64, error) {
	res, err := d.Exec(
		"INSERT INTO storage_destinations (name, type, config) VALUES (?, ?, ?)",
		dest.Name, dest.Type, dest.Config,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetStorageDestination(id int64) (StorageDestination, error) {
	var dest StorageDestination
	err := d.QueryRow(
		"SELECT id, name, type, config, created_at, updated_at FROM storage_destinations WHERE id = ?", id,
	).Scan(&dest.ID, &dest.Name, &dest.Type, &dest.Config, &dest.CreatedAt, &dest.UpdatedAt)
	if err == sql.ErrNoRows {
		return dest, ErrNotFound
	}
	return dest, err
}

func (d *DB) ListStorageDestinations() ([]StorageDestination, error) {
	rows, err := d.Query("SELECT id, name, type, config, created_at, updated_at FROM storage_destinations ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dests []StorageDestination
	for rows.Next() {
		var dest StorageDestination
		if err := rows.Scan(&dest.ID, &dest.Name, &dest.Type, &dest.Config, &dest.CreatedAt, &dest.UpdatedAt); err != nil {
			return nil, err
		}
		dests = append(dests, dest)
	}
	return dests, rows.Err()
}

func (d *DB) UpdateStorageDestination(dest StorageDestination) error {
	_, err := d.Exec(
		"UPDATE storage_destinations SET name=?, type=?, config=?, updated_at=CURRENT_TIMESTAMP WHERE id=?",
		dest.Name, dest.Type, dest.Config, dest.ID,
	)
	return err
}

func (d *DB) DeleteStorageDestination(id int64) error {
	_, err := d.Exec("DELETE FROM storage_destinations WHERE id = ?", id)
	return err
}

// CountJobsByStorageDestID returns the number of jobs that reference the
// given storage destination.
func (d *DB) CountJobsByStorageDestID(storageDestID int64) (int, error) {
	var count int
	err := d.QueryRow("SELECT COUNT(*) FROM jobs WHERE storage_dest_id = ?", storageDestID).Scan(&count)
	return count, err
}

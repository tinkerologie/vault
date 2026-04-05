package db

// GetSetting retrieves a setting value by key. Returns the default if not found.
func (d *DB) GetSetting(key, defaultVal string) (string, error) {
	var val string
	err := d.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&val)
	if err != nil {
		return defaultVal, nil //nolint:nilerr // missing key is not an error
	}
	return val, nil
}

// SetSetting upserts a setting value by key.
func (d *DB) SetSetting(key, value string) error {
	_, err := d.Exec(
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	)
	return err
}

// GetAllSettings returns all settings as a key-value map.
func (d *DB) GetAllSettings() (map[string]string, error) {
	rows, err := d.Query("SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		settings[k] = v
	}
	return settings, rows.Err()
}

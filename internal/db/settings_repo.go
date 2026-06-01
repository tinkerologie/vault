package db

import (
	"database/sql"
	"errors"
	"strconv"
)

// GetSetting retrieves a setting value by key. Returns the default if not found.
// Real database errors are propagated to the caller.
func (d *DB) GetSetting(key, defaultVal string) (string, error) {
	var val string
	err := d.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&val)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return defaultVal, nil
		}
		return defaultVal, err
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

// GetSettingInt retrieves a setting value parsed as int. Returns defaultVal
// if missing or unparsable.
func (d *DB) GetSettingInt(key string, defaultVal int) (int, error) {
	s, err := d.GetSetting(key, "")
	if err != nil {
		return defaultVal, err
	}
	if s == "" {
		return defaultVal, nil
	}
	v, parseErr := strconv.Atoi(s)
	if parseErr != nil {
		return defaultVal, nil // silent fallback — config errors shouldn't crash
	}
	return v, nil
}

// GetSettingBool reads a boolean setting. It returns (def, nil) when the key is
// absent, the parsed value with nil error when present, and (def, err) when the
// underlying read fails so callers can surface genuine DB errors.
func (d *DB) GetSettingBool(key string, def bool) (bool, error) {
	v, err := d.GetSetting(key, "")
	if err != nil {
		return def, err
	}
	if v == "" {
		return def, nil
	}
	return v == "true" || v == "1", nil
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

package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
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

// SFTPHostKeyPinPrefix prefixes the settings keys holding trust-on-first-use
// SFTP host key fingerprints, one per host:port.
const SFTPHostKeyPinPrefix = "sftp_host_key_pin:"

// PinSFTPHostKey records fingerprint as the host key for address unless one
// is already on record, and returns the fingerprint on record. The conditional
// insert is atomic and a pin is never updated here, so concurrent first
// connections agree on one pin.
func (d *DB) PinSFTPHostKey(address, fingerprint string) (string, error) {
	key := SFTPHostKeyPinPrefix + address
	res, err := d.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING", key, fingerprint)
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		log.Printf("SFTP: trusting host key %s for %s on first connection; later connections must present the same key", fingerprint, address) // #nosec G706 -- admin-configured address; fingerprint is base64
		d.LogActivity("info", "storage", fmt.Sprintf("SFTP host key pinned on first connection: %s is now trusted for %s; a different key will be refused", fingerprint, address), "")
		return fingerprint, nil
	}
	return d.GetSetting(key, "")
}

// UnpinSFTPHostKey forgets the pin for address.
func (d *DB) UnpinSFTPHostKey(address string) error {
	_, err := d.Exec("DELETE FROM settings WHERE key = ?", SFTPHostKeyPinPrefix+address)
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

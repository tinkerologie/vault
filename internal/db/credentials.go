package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/tinkerologie/vault/internal/credentials"
	"github.com/tinkerologie/vault/internal/crypto"
)

// settingSealScrubPending stays "1" from the moment secrets are sealed until
// every older copy of the database has been rewritten. It survives a crash, so
// the next boot finishes the job.
const settingSealScrubPending = "secrets_seal_scrub_pending"

const (
	selectStorageConfig     = `SELECT COALESCE(config, '{}') FROM storage_destinations WHERE id = ?`
	selectReplicationConfig = `SELECT COALESCE(config, '{}') FROM replication_sources WHERE id = ?`
)

// sealedTables lists the tables whose config column holds secrets.
var sealedTables = []struct{ list, update string }{
	{`SELECT id, COALESCE(config, '{}') FROM storage_destinations`, `UPDATE storage_destinations SET config = ? WHERE id = ?`},
	{`SELECT id, COALESCE(config, '{}') FROM replication_sources`, `UPDATE replication_sources SET config = ? WHERE id = ?`},
}

// SetCredentialKey gives the repository the vault.key used to seal and open
// storage and replication secrets. Without it sealed values read as locked. The
// key outlives Reopen, so a restored database is handled the same way.
func (d *DB) SetCredentialKey(key []byte) {
	k := append([]byte(nil), key...)
	d.credKey.Store(&k)
}

func (d *DB) credentialKey() []byte {
	if k := d.credKey.Load(); k != nil {
		return *k
	}
	return nil
}

func (d *DB) sealConfig(config string) (string, error) {
	return credentials.Seal(d.credentialKey(), config)
}

func (d *DB) openConfig(stored string) string {
	return credentials.Open(d.credentialKey(), stored)
}

// sealForUpdate seals an edited config. A redaction marker that reached this far
// takes the stored value, so no write path can replace a secret with the marker.
func (d *DB) sealForUpdate(config, selectStored string, id int64) (string, error) {
	var stored string
	err := d.QueryRow(selectStored, id).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read stored config: %w", err)
	}
	return d.sealConfig(credentials.Preserve(config, stored))
}

// SealSecretsAtRest seals every plaintext storage and replication secret and the
// legacy backup passphrase in one transaction, so a crash leaves the old state
// intact. It is idempotent. It then rewrites the file, because SQLite keeps
// deleted plaintext in free pages and the WAL. It returns the rows changed.
func (d *DB) SealSecretsAtRest() (int, error) {
	key := d.credentialKey()
	if len(key) == 0 {
		return 0, nil
	}
	changed, err := d.sealRows(key)
	if err != nil {
		return 0, err
	}
	pending, err := d.GetSetting(settingSealScrubPending, "")
	if err != nil {
		return changed, fmt.Errorf("read seal state: %w", err)
	}
	if pending != "1" {
		return changed, nil
	}
	for _, stmt := range []string{"VACUUM", "PRAGMA wal_checkpoint(TRUNCATE)"} {
		if _, err := d.Exec(stmt); err != nil {
			return changed, fmt.Errorf("rewrite after sealing: %w", err)
		}
	}
	return changed, nil
}

func (d *DB) sealRows(key []byte) (int, error) {
	tx, err := d.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin sealing: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	changed := 0
	for _, table := range sealedTables {
		n, err := sealTable(tx, key, table.list, table.update)
		if err != nil {
			return 0, fmt.Errorf("seal config rows: %w", err)
		}
		changed += n
	}
	n, err := sealLegacyPassphrase(tx, key)
	if err != nil {
		return 0, err
	}
	changed += n
	if changed > 0 {
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, '1')
			ON CONFLICT(key) DO UPDATE SET value = '1'`, settingSealScrubPending); err != nil {
			return 0, fmt.Errorf("record seal state: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit sealing: %w", err)
	}
	return changed, nil
}

func sealTable(tx *sql.Tx, key []byte, list, update string) (int, error) {
	configs, err := readConfigs(tx, list)
	if err != nil {
		return 0, err
	}
	changed := 0
	for id, config := range configs {
		sealed, err := credentials.Seal(key, config)
		if err != nil {
			return 0, fmt.Errorf("row %d: %w", id, err)
		}
		if sealed == config {
			continue
		}
		if _, err := tx.Exec(update, sealed, id); err != nil {
			return 0, fmt.Errorf("row %d: %w", id, err)
		}
		changed++
	}
	return changed, nil
}

// readConfigs finishes reading before any update, because the transaction has
// one connection.
func readConfigs(tx *sql.Tx, list string) (map[int64]string, error) {
	rows, err := tx.Query(list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	configs := map[int64]string{}
	for rows.Next() {
		var id int64
		var config string
		if err := rows.Scan(&id, &config); err != nil {
			return nil, err
		}
		configs[id] = config
	}
	return configs, rows.Err()
}

func sealLegacyPassphrase(tx *sql.Tx, key []byte) (int, error) {
	var plaintext string
	err := tx.QueryRow(`SELECT value FROM settings WHERE key = 'encryption_passphrase'`).Scan(&plaintext)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && plaintext == "") {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read legacy passphrase: %w", err)
	}
	sealed, err := crypto.Seal(key, plaintext)
	if err != nil {
		return 0, fmt.Errorf("seal legacy passphrase: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('encryption_passphrase_sealed', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, sealed); err != nil {
		return 0, fmt.Errorf("store sealed passphrase: %w", err)
	}
	if _, err := tx.Exec(`UPDATE settings SET value = '' WHERE key = 'encryption_passphrase'`); err != nil {
		return 0, fmt.Errorf("clear legacy passphrase: %w", err)
	}
	return 1, nil
}

// CountLockedCredentials returns how many storage and replication rows hold
// secrets this server's vault.key cannot open.
func (d *DB) CountLockedCredentials() (int, error) {
	dests, err := d.ListStorageDestinations()
	if err != nil {
		return 0, err
	}
	sources, err := d.ListReplicationSources()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, dest := range dests {
		if credentials.IsLocked(dest.Config) {
			n++
		}
	}
	for _, src := range sources {
		if credentials.IsLocked(src.Config) {
			n++
		}
	}
	return n, nil
}

// SealCopy seals the secrets in an older database file in place, keeping its
// mtime because boot-time restore picks the freshest copy by mtime. A missing
// file is not an error.
func SealCopy(path string, key []byte) error {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		if os.IsNotExist(err) || err == nil {
			return nil
		}
		return err
	}
	copyDB, err := Open(path)
	if err != nil {
		return err
	}
	copyDB.SetCredentialKey(key)
	_, sealErr := copyDB.SealSecretsAtRest()
	closeErr := copyDB.Close()
	if err := errors.Join(sealErr, closeErr); err != nil {
		return err
	}
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	return os.Chtimes(path, time.Now(), info.ModTime())
}

// SealPersistentCopies replaces plaintext left in older database copies once the
// working database is sealed: the snapshot and USB shadow are re-flushed, and the
// rotated snapshots and extra paths are sealed in place. On any failure the
// pending marker stays set and the next boot retries. sm is nil outside hybrid
// mode. Remote copies under _vault cannot be reached here; they age out through
// normal DB backup pruning.
func (d *DB) SealPersistentCopies(ctx context.Context, sm *SnapshotManager, extraPaths []string) error {
	pending, err := d.GetSetting(settingSealScrubPending, "")
	if err != nil {
		return fmt.Errorf("read seal state: %w", err)
	}
	if pending != "1" {
		return nil
	}
	key := d.credentialKey()
	var errs []error
	paths := append([]string(nil), extraPaths...)
	if sm != nil {
		if err := sm.FlushToUSB(); err != nil {
			errs = append(errs, fmt.Errorf("flush sealed database: %w", err))
		}
		rotatedDir := filepath.Join(filepath.Dir(sm.SnapshotPath()), "rotated")
		rotated, err := os.ReadDir(rotatedDir)
		if err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
		for _, e := range rotated {
			paths = append(paths, filepath.Join(rotatedDir, e.Name()))
		}
	}
	for _, p := range paths {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if err := SealCopy(p, key); err != nil {
			errs = append(errs, fmt.Errorf("seal copy %s: %w", p, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if err := d.SetSetting(settingSealScrubPending, ""); err != nil {
		return fmt.Errorf("clear seal state: %w", err)
	}
	if sm != nil {
		if err := sm.FlushToUSB(); err != nil {
			log.Printf("Warning: flush after sealing database copies: %v", err)
		}
	}
	return nil
}

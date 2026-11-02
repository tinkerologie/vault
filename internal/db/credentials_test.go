package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/credentials"
	"github.com/tinkerologie/vault/internal/crypto"
)

var (
	credKeyA = []byte("0123456789abcdef0123456789abcdef")
	credKeyB = []byte("ffffffffffffffffffffffffffffffff")
)

const plaintextSecret = "PlaintextS3cretValueForScan"

func openCredDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func rawConfig(t *testing.T, d *DB, query string, id int64) string {
	t.Helper()
	var s string
	if err := d.QueryRow(query, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// seedPlaintext creates rows as an older version stored them.
func seedPlaintext(t *testing.T, d *DB) (destID, srcID int64) {
	t.Helper()
	destID, err := d.CreateStorageDestination(StorageDestination{Name: "nas", Type: "smb", Config: `{"host":"nas","password":"` + plaintextSecret + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	srcID, err = d.CreateReplicationSource(ReplicationSource{Name: "peer", URL: "http://peer", Config: `{"api_key":"` + plaintextSecret + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("encryption_passphrase", plaintextSecret); err != nil {
		t.Fatal(err)
	}
	return destID, srcID
}

func fileHasSecret(t *testing.T, path string) bool {
	t.Helper()
	for _, p := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(b), plaintextSecret) {
			return true
		}
	}
	return false
}

func TestSealSecretsAtRestMigratesAndScrubsFile(t *testing.T) {
	d := openCredDB(t)
	destID, srcID := seedPlaintext(t, d)
	// A destination deleted long ago leaves its plaintext in a free page, out of
	// reach of the row rewrite. Only rewriting the file removes it.
	pad := strings.Repeat("x", 9000)
	goneID, err := d.CreateStorageDestination(StorageDestination{Name: "gone", Type: "smb", Config: `{"password":"` + plaintextSecret + pad + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteStorageDestination(goneID); err != nil {
		t.Fatal(err)
	}
	if !fileHasSecret(t, d.Path()) {
		t.Fatal("test premise: plaintext should be in the file before migration")
	}
	d.SetCredentialKey(credKeyA)
	n, err := d.SealSecretsAtRest()
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("changed = %d, want 3", n)
	}
	if fileHasSecret(t, d.Path()) {
		t.Fatal("plaintext survives in the database file or WAL")
	}
	if raw := rawConfig(t, d, selectStorageConfig, destID); strings.Contains(raw, plaintextSecret) || !strings.Contains(raw, `"host":"nas"`) {
		t.Fatalf("raw storage config = %s", raw)
	}
	dest, err := d.GetStorageDestination(destID)
	if err != nil || !strings.Contains(dest.Config, plaintextSecret) {
		t.Fatalf("read did not open the secret: %v", err)
	}
	src, err := d.GetReplicationSource(srcID)
	if err != nil || !strings.Contains(src.Config, plaintextSecret) {
		t.Fatalf("read did not open the key: %v", err)
	}
	sealed, _ := d.GetSetting("encryption_passphrase_sealed", "")
	if got, err := crypto.Unseal(credKeyA, sealed); err != nil || got != plaintextSecret {
		t.Fatalf("sealed passphrase = %q, %v", got, err)
	}
	if legacy, _ := d.GetSetting("encryption_passphrase", ""); legacy != "" {
		t.Fatal("legacy passphrase not cleared")
	}

	// Idempotent: nothing changes and existing ciphertext is not rewritten.
	before := rawConfig(t, d, selectStorageConfig, destID)
	if n, err := d.SealSecretsAtRest(); err != nil || n != 0 {
		t.Fatalf("second run changed %d (err %v)", n, err)
	}
	if rawConfig(t, d, selectStorageConfig, destID) != before {
		t.Fatal("ciphertext rewritten on second run")
	}
}

func TestSealSecretsAtRestIsAllOrNothing(t *testing.T) {
	d := openCredDB(t)
	destID, _ := seedPlaintext(t, d)
	if _, err := d.Exec(`CREATE TRIGGER fail_seal BEFORE UPDATE OF config ON replication_sources
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	d.SetCredentialKey(credKeyA)
	if _, err := d.SealSecretsAtRest(); err == nil {
		t.Fatal("want error")
	}
	if raw := rawConfig(t, d, selectStorageConfig, destID); !strings.Contains(raw, plaintextSecret) {
		t.Fatalf("storage row sealed despite rollback: %s", raw)
	}
	if legacy, _ := d.GetSetting("encryption_passphrase", ""); legacy != plaintextSecret {
		t.Fatal("legacy passphrase lost in a failed migration")
	}
	if _, err := d.Exec(`DROP TRIGGER fail_seal`); err != nil {
		t.Fatal(err)
	}
	if n, err := d.SealSecretsAtRest(); err != nil || n != 3 {
		t.Fatalf("retry changed %d (err %v)", n, err)
	}
}

func TestSealSecretsAtRestWithoutKeyIsANoOp(t *testing.T) {
	d := openCredDB(t)
	seedPlaintext(t, d)
	if n, err := d.SealSecretsAtRest(); err != nil || n != 0 {
		t.Fatalf("changed %d (err %v)", n, err)
	}
}

func TestWritesAreSealedAndMarkerKeepsSecret(t *testing.T) {
	d := openCredDB(t)
	d.SetCredentialKey(credKeyA)
	id, err := d.CreateStorageDestination(StorageDestination{Name: "n", Type: "smb", Config: `{"host":"a","password":"` + plaintextSecret + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if raw := rawConfig(t, d, selectStorageConfig, id); strings.Contains(raw, plaintextSecret) {
		t.Fatalf("created row is plaintext: %s", raw)
	}
	dest, _ := d.GetStorageDestination(id)
	dest.Config = `{"host":"b","password":"` + credentials.RedactionMarker + `"}`
	if err := d.UpdateStorageDestination(dest); err != nil {
		t.Fatal(err)
	}
	dest, _ = d.GetStorageDestination(id)
	if !strings.Contains(dest.Config, plaintextSecret) || !strings.Contains(dest.Config, `"host":"b"`) {
		t.Fatalf("after marker update: %s", dest.Config)
	}

	src, err := d.CreateReplicationSource(ReplicationSource{Name: "p", URL: "http://p", Config: `{"api_key":"` + plaintextSecret + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if raw := rawConfig(t, d, selectReplicationConfig, src); strings.Contains(raw, plaintextSecret) {
		t.Fatalf("created source is plaintext: %s", raw)
	}
	if err := d.UpdateReplicationSource(ReplicationSource{ID: src, Name: "p", Type: "remote_vault", URL: "http://p", Config: `{"api_key":"` + credentials.RedactionMarker + `"}`}); err != nil {
		t.Fatal(err)
	}
	got, _ := d.GetReplicationSource(src)
	if !strings.Contains(got.Config, plaintextSecret) {
		t.Fatalf("after marker update: %s", got.Config)
	}
}

func TestWritesFailClosedOnBadKey(t *testing.T) {
	d := openCredDB(t)
	id, err := d.CreateStorageDestination(StorageDestination{Name: "n", Type: "smb", Config: `{"host":"a"}`})
	if err != nil {
		t.Fatal(err)
	}
	srcID, err := d.CreateReplicationSource(ReplicationSource{Name: "p", URL: "http://p", Config: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	d.SetCredentialKey([]byte("short"))
	cfg := `{"password":"` + plaintextSecret + `"}`
	if _, err := d.CreateStorageDestination(StorageDestination{Name: "x", Type: "smb", Config: cfg}); err == nil {
		t.Error("create stored a secret it could not seal")
	}
	if err := d.UpdateStorageDestination(StorageDestination{ID: id, Name: "n", Type: "smb", Config: cfg}); err == nil {
		t.Error("update stored a secret it could not seal")
	}
	if _, err := d.CreateReplicationSource(ReplicationSource{Name: "y", URL: "http://y", Config: `{"api_key":"k"}`}); err == nil {
		t.Error("create source stored a key it could not seal")
	}
	if err := d.UpdateReplicationSource(ReplicationSource{ID: srcID, Name: "p", URL: "http://p", Config: `{"api_key":"k"}`}); err == nil {
		t.Error("update source stored a key it could not seal")
	}
}

func TestLockedRowFailsClosedAndSurvivesEdits(t *testing.T) {
	d := openCredDB(t)
	d.SetCredentialKey(credKeyA)
	lockedID, err := d.CreateStorageDestination(StorageDestination{Name: "locked", Type: "smb", Config: `{"host":"a","password":"` + plaintextSecret + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateReplicationSource(ReplicationSource{Name: "p", URL: "http://p", Config: `{"api_key":"k"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateStorageDestination(StorageDestination{Name: "local", Type: "local", Config: `{"path":"/mnt/x"}`}); err != nil {
		t.Fatal(err)
	}

	d.SetCredentialKey(credKeyB)
	dests, err := d.ListStorageDestinations()
	if err != nil || len(dests) != 2 {
		t.Fatalf("one locked row must not fail the listing: %v (%d rows)", err, len(dests))
	}
	if n, err := d.CountLockedCredentials(); err != nil || n != 2 {
		t.Fatalf("locked = %d (err %v), want 2", n, err)
	}
	dest, _ := d.GetStorageDestination(lockedID)
	if !credentials.IsLocked(dest.Config) || strings.Contains(dest.Config, plaintextSecret) {
		t.Fatalf("locked row = %s", dest.Config)
	}

	// An unrelated edit (a path remap, a rename) keeps the ciphertext.
	dest.Name = "renamed"
	if err := d.UpdateStorageDestination(dest); err != nil {
		t.Fatal(err)
	}
	d.SetCredentialKey(credKeyA)
	dest, _ = d.GetStorageDestination(lockedID)
	if !strings.Contains(dest.Config, plaintextSecret) {
		t.Fatalf("restoring the original key should unlock the row: %s", dest.Config)
	}
	if n, _ := d.CountLockedCredentials(); n != 0 {
		t.Fatalf("locked = %d after key restored", n)
	}
}

func TestReopenSealsARestoredPlaintextDatabase(t *testing.T) {
	d := openCredDB(t)
	d.SetCredentialKey(credKeyA)
	destID, _ := seedPlaintextRaw(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Reopen(); err != nil {
		t.Fatal(err)
	}
	if raw := rawConfig(t, d, selectStorageConfig, destID); strings.Contains(raw, plaintextSecret) {
		t.Fatalf("restored plaintext not sealed: %s", raw)
	}
	dest, _ := d.GetStorageDestination(destID)
	if !strings.Contains(dest.Config, plaintextSecret) {
		t.Fatal("key lost across Reopen")
	}
}

// seedPlaintextRaw writes plaintext past the sealing layer, as a restored
// pre-upgrade database would hold it.
func seedPlaintextRaw(t *testing.T, d *DB) (int64, string) {
	t.Helper()
	cfg := `{"host":"nas","password":"` + plaintextSecret + `"}`
	res, err := d.Exec(`INSERT INTO storage_destinations (name, type, config) VALUES ('old', 'smb', ?)`, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id, cfg
}

func TestSealCopyKeepsMtimeAndIgnoresMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "copy.db")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedPlaintext(t, c)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := SealCopy(path, credKeyA); err != nil {
		t.Fatal(err)
	}
	if fileHasSecret(t, path) {
		t.Fatal("copy still holds plaintext")
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("mtime = %v, want %v (err %v)", info.ModTime(), old, err)
	}
	if err := SealCopy(filepath.Join(dir, "missing.db"), credKeyA); err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if err := SealCopy(dir, credKeyA); err != nil {
		t.Fatalf("directory: %v", err)
	}
}

func TestSealPersistentCopies(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	seedPlaintext(t, d)

	snapDir := filepath.Join(dir, "snap")
	sm := NewSnapshotManager(d, filepath.Join(snapDir, "vault.db"), filepath.Join(snapDir, "vault.db"))
	usb := filepath.Join(dir, "usb", "vault.db.backup")
	sm.SetUSBBackupPath(usb)
	// Two plaintext generations: one leaves a rotated copy behind.
	if err := sm.FlushToUSB(); err != nil {
		t.Fatal(err)
	}
	migrated := filepath.Join(dir, "vault.db.migrated")
	if err := copyFile(usb, migrated); err != nil {
		t.Fatal(err)
	}
	rotated, _ := filepath.Glob(filepath.Join(snapDir, "rotated", "*"))
	if len(rotated) == 0 || !fileHasSecret(t, rotated[0]) {
		t.Fatalf("test premise: rotated plaintext copy expected, got %v", rotated)
	}

	d.SetCredentialKey(credKeyA)
	if _, err := d.SealSecretsAtRest(); err != nil {
		t.Fatal(err)
	}

	// A copy that cannot be sealed keeps the marker for the next boot.
	bad := filepath.Join(dir, "corrupt.db")
	if err := os.WriteFile(bad, []byte("not a database, long enough to be read as one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.SealPersistentCopies(context.Background(), sm, []string{migrated, bad}); err == nil {
		t.Fatal("want error for corrupt copy")
	}
	if v, _ := d.GetSetting(settingSealScrubPending, ""); v != "1" {
		t.Fatal("marker cleared despite failure")
	}

	if err := d.SealPersistentCopies(context.Background(), sm, []string{migrated}); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetSetting(settingSealScrubPending, ""); v != "" {
		t.Fatal("marker not cleared")
	}
	all, _ := filepath.Glob(filepath.Join(snapDir, "rotated", "*"))
	all = append(all, filepath.Join(snapDir, "vault.db"), usb, migrated)
	for _, p := range all {
		if fileHasSecret(t, p) {
			t.Errorf("%s still holds plaintext", p)
		}
	}

	// Nothing pending: a later boot does no work, even with a bad path listed.
	if err := d.SealPersistentCopies(context.Background(), sm, []string{bad}); err != nil {
		t.Fatalf("not pending: %v", err)
	}
}

func TestSealPersistentCopiesStopsOnCancel(t *testing.T) {
	d := openCredDB(t)
	seedPlaintext(t, d)
	d.SetCredentialKey(credKeyA)
	if _, err := d.SealSecretsAtRest(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.SealPersistentCopies(ctx, nil, []string{filepath.Join(t.TempDir(), "x")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestCredentialErrorsOnClosedDatabase(t *testing.T) {
	d := openCredDB(t)
	id, err := d.CreateStorageDestination(StorageDestination{Name: "n", Type: "smb", Config: `{"host":"a"}`})
	if err != nil {
		t.Fatal(err)
	}
	d.SetCredentialKey(credKeyA)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SealSecretsAtRest(); err == nil {
		t.Error("SealSecretsAtRest on a closed database")
	}
	if err := d.UpdateStorageDestination(StorageDestination{ID: id, Name: "n", Type: "smb", Config: `{}`}); err == nil {
		t.Error("update on a closed database")
	}
	if _, err := d.CountLockedCredentials(); err == nil {
		t.Error("count on a closed database")
	}
	if err := d.SealPersistentCopies(context.Background(), nil, nil); err == nil {
		t.Error("copies on a closed database")
	}
}

func TestSealCopyReportsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.db")
	if err := os.WriteFile(path, []byte(strings.Repeat("not a database ", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SealCopy(path, credKeyA); err == nil {
		t.Fatal("want error")
	}
}

// Every step of the migration shares one transaction: when any of them fails,
// nothing is sealed and the plaintext stays usable for the next attempt.
func TestSealRollsBackWhenASettingsWriteFails(t *testing.T) {
	triggers := map[string]string{
		"sealed passphrase": `CREATE TRIGGER f BEFORE INSERT ON settings WHEN NEW.key = 'encryption_passphrase_sealed' BEGIN SELECT RAISE(ABORT, 'boom'); END`,
		"sealed update":     `CREATE TRIGGER f BEFORE UPDATE ON settings WHEN NEW.key = 'encryption_passphrase_sealed' BEGIN SELECT RAISE(ABORT, 'boom'); END`,
		"clear passphrase":  `CREATE TRIGGER f BEFORE UPDATE ON settings WHEN NEW.key = 'encryption_passphrase' BEGIN SELECT RAISE(ABORT, 'boom'); END`,
		"pending marker":    `CREATE TRIGGER f BEFORE INSERT ON settings WHEN NEW.key = '` + settingSealScrubPending + `' BEGIN SELECT RAISE(ABORT, 'boom'); END`,
	}
	for name, trigger := range triggers {
		t.Run(name, func(t *testing.T) {
			d := openCredDB(t)
			destID, _ := seedPlaintext(t, d)
			if err := d.SetSetting("encryption_passphrase_sealed", "old"); err != nil {
				t.Fatal(err)
			}
			if _, err := d.Exec(`DELETE FROM settings WHERE key = 'encryption_passphrase_sealed' AND ? = 'sealed passphrase'`, name); err != nil {
				t.Fatal(err)
			}
			if _, err := d.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			d.SetCredentialKey(credKeyA)
			if _, err := d.SealSecretsAtRest(); err == nil {
				t.Fatal("want error")
			}
			if raw := rawConfig(t, d, selectStorageConfig, destID); !strings.Contains(raw, plaintextSecret) {
				t.Fatalf("row sealed despite rollback: %s", raw)
			}
		})
	}
}

func TestSealFailsCleanlyWithABadKey(t *testing.T) {
	d := openCredDB(t)
	seedPlaintext(t, d)
	d.SetCredentialKey([]byte("short"))
	if _, err := d.SealSecretsAtRest(); err == nil {
		t.Fatal("rows: want error")
	}
	if _, err := d.Exec(`DELETE FROM storage_destinations`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`DELETE FROM replication_sources`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SealSecretsAtRest(); err == nil {
		t.Fatal("legacy passphrase: want error")
	}
}

func TestSealPersistentCopiesReportsSnapshotFailures(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	seedPlaintext(t, d)
	d.SetCredentialKey(credKeyA)
	if _, err := d.SealSecretsAtRest(); err != nil {
		t.Fatal(err)
	}
	// The snapshot directory is a file, so the flush and rotated listing fail.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sm := NewSnapshotManager(d, filepath.Join(blocker, "vault.db"), filepath.Join(blocker, "vault.db"))
	if err := d.SealPersistentCopies(context.Background(), sm, nil); err == nil {
		t.Fatal("want error")
	}
	if v, _ := d.GetSetting(settingSealScrubPending, ""); v != "1" {
		t.Fatal("marker cleared despite failure")
	}
}

// Older copies are inspected once even when no live row needs sealing: the
// credential may have been deleted, or sealed by an earlier version.
func TestOlderCopiesAreInspectedWithoutLiveChanges(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "vault.db.migrated")
	c, err := Open(older)
	if err != nil {
		t.Fatal(err)
	}
	seedPlaintext(t, c)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	d := openCredDB(t)
	d.SetCredentialKey(credKeyA)
	if n, err := d.SealSecretsAtRest(); err != nil || n != 0 {
		t.Fatalf("changed %d (err %v)", n, err)
	}
	if err := d.SealPersistentCopies(context.Background(), nil, []string{older}); err != nil {
		t.Fatal(err)
	}
	if fileHasSecret(t, older) {
		t.Fatal("older copy still holds plaintext")
	}

	// Done once: a later start with nothing to seal does no copy work.
	if _, err := d.SealSecretsAtRest(); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetSetting(settingSealScrubPending, ""); v != "" {
		t.Fatalf("copy work repeated, pending = %q", v)
	}
}

func TestMalformedStoredConfigIsSealed(t *testing.T) {
	d := openCredDB(t)
	id, _ := seedPlaintextRaw(t, d)
	bad := `{"password":"` + plaintextSecret + `"}trailing`
	if _, err := d.Exec(`UPDATE storage_destinations SET config = ? WHERE id = ?`, bad, id); err != nil {
		t.Fatal(err)
	}
	d.SetCredentialKey(credKeyA)
	if _, err := d.SealSecretsAtRest(); err != nil {
		t.Fatal(err)
	}
	if raw := rawConfig(t, d, selectStorageConfig, id); strings.Contains(raw, plaintextSecret) {
		t.Fatalf("malformed config left in plaintext: %s", raw)
	}
	dest, err := d.GetStorageDestination(id)
	if err != nil || dest.Config != bad {
		t.Fatalf("config = %q (err %v)", dest.Config, err)
	}
}

package db

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	snapshotEnvelopeVersion = 1
	snapshotEnvelopeSuffix  = ".auth"
	// rotatedEnvelopeDir keeps rotated envelopes out of rotated/, every file of
	// which is treated as a database copy.
	rotatedEnvelopeDir = "rotated-auth"
	// snapshotEnrolledMarker sits beside vault.key. Once present, a snapshot with
	// no valid envelope is refused, so removing an envelope cannot downgrade the
	// check without write access to the flash drive, which already means root.
	snapshotEnrolledMarker = "snapshot-auth.enrolled"
	snapshotEnvelopeMax    = 4096
)

// snapshotEnvelope authenticates one snapshot file. It sits beside the snapshot
// and adds nothing to the database file, so older Vault versions and existing
// snapshots are unaffected. Using a subkey of vault.key binds it to this server.
type snapshotEnvelope struct {
	Version int    `json:"version"`
	SavedAt int64  `json:"saved_at_unix_nano"`
	SHA256  string `json:"sha256"`
	MAC     string `json:"mac"`
}

// SnapshotAuth signs and verifies database snapshots.
type SnapshotAuth struct {
	key           []byte
	markerPath    string
	allowUnsigned bool
	// readHook replaces readEnvelope in tests that race the envelope.
	readHook func(string) (snapshotEnvelope, error)
}

func (a *SnapshotAuth) read(path string) (snapshotEnvelope, error) {
	if a.readHook != nil {
		return a.readHook(path)
	}
	return a.readEnvelope(path)
}

// NewSnapshotAuth derives the signing key from the server key. anchorDir is the
// directory holding vault.key. allowUnsigned is the operator's override for
// restoring a snapshot with no envelope once enrolled.
func NewSnapshotAuth(serverKey []byte, anchorDir string, allowUnsigned bool) *SnapshotAuth {
	mac := hmac.New(sha256.New, serverKey)
	mac.Write([]byte("vault snapshot authentication v1"))
	return &SnapshotAuth{key: mac.Sum(nil), markerPath: filepath.Join(anchorDir, snapshotEnrolledMarker), allowUnsigned: allowUnsigned}
}

func (a *SnapshotAuth) enrolled() bool {
	_, err := os.Lstat(a.markerPath)
	return err == nil
}

func (a *SnapshotAuth) mac(savedAt int64, sum string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(strconv.Itoa(snapshotEnvelopeVersion) + "|" + strconv.FormatInt(savedAt, 10) + "|" + sum))
	return hex.EncodeToString(m.Sum(nil))
}

// envelopePath names a snapshot's envelope.
func envelopePath(snapshot string) string {
	dir, name := filepath.Split(snapshot)
	if filepath.Base(filepath.Clean(dir)) == "rotated" {
		return filepath.Join(filepath.Dir(filepath.Clean(dir)), rotatedEnvelopeDir, name+snapshotEnvelopeSuffix)
	}
	return snapshot + snapshotEnvelopeSuffix
}

// Sign writes the envelope for a snapshot this server just produced and enrolls
// the server. SavedAt is the file's mtime, so an aligned USB copy still ties with
// it during restore ordering.
func (a *SnapshotAuth) Sign(path string) error {
	if !filepath.IsAbs(path) || strings.Contains(path, "..") {
		return fmt.Errorf("refusing suspicious snapshot path %q", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	in, err := openRegular(path)
	if err != nil {
		return err
	}
	defer in.Close()
	// The snapshot directory may be writable by others. Anything they put at
	// the path in place of the file this process just wrote is theirs, not ours.
	if opened, statErr := in.Stat(); statErr != nil || !ownedByProcess(opened) {
		return fmt.Errorf("%s is not owned by this process", path)
	}
	sum, err := hashReader(in)
	if err != nil {
		return err
	}
	savedAt := info.ModTime().UnixNano()
	env := snapshotEnvelope{Version: snapshotEnvelopeVersion, SavedAt: savedAt, SHA256: sum, MAC: a.mac(savedAt, sum)}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(envelopePath(path), data); err != nil {
		return err
	}
	if a.enrolled() {
		return nil
	}
	return writeFileAtomic(a.markerPath, []byte("1\n"))
}

// copyEnvelope gives a rotated copy its source's envelope; the two files are
// byte-identical and the envelope names no path.
func (a *SnapshotAuth) copyEnvelope(src, dst string) error {
	from := envelopePath(src)
	if !filepath.IsAbs(from) || strings.Contains(from, "..") {
		return fmt.Errorf("refusing suspicious snapshot path %q", from)
	}
	data, err := os.ReadFile(from) // #nosec G304 -- validated snapshot path
	if err != nil {
		return err
	}
	return writeFileAtomic(envelopePath(dst), data)
}

func (a *SnapshotAuth) readEnvelope(path string) (snapshotEnvelope, error) {
	var env snapshotEnvelope
	f, err := openRegular(envelopePath(path))
	if err != nil {
		return env, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, snapshotEnvelopeMax+1))
	if err != nil {
		return env, err
	}
	if len(data) > snapshotEnvelopeMax {
		return env, errors.New("envelope is too large")
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return env, fmt.Errorf("parsing envelope: %w", err)
	}
	if env.Version != snapshotEnvelopeVersion || !hmac.Equal([]byte(env.MAC), []byte(a.mac(env.SavedAt, env.SHA256))) {
		return env, errors.New("envelope signature does not match this server")
	}
	return env, nil
}

// Admissible reports whether a candidate may be considered at all, without
// hashing it. Before enrollment everything passes with a warning: a first boot
// after upgrading, or on a replacement flash drive with a new key, must still
// find its existing snapshots.
func (a *SnapshotAuth) Admissible(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	_, envErr := a.read(path)
	return a.unsignedDecision(path, envErr)
}

// unsignedDecision applies the unsigned-snapshot policy to the result of one
// envelope read. A nil envErr means the envelope was valid.
func (a *SnapshotAuth) unsignedDecision(path string, envErr error) error {
	switch {
	case envErr == nil:
		return nil
	case !a.enrolled():
		log.Printf("Warning: snapshot %s is not authenticated (%v); accepting it because this server has not signed a snapshot yet", path, envErr)
		return nil
	case errors.Is(envErr, os.ErrNotExist) && a.allowUnsigned:
		log.Printf("Warning: restoring unsigned snapshot %s because ALLOW_UNSIGNED_SNAPSHOT is set", path)
		return nil
	case errors.Is(envErr, os.ErrNotExist):
		return errors.New("it has no authentication envelope. If this is your own older snapshot, set ALLOW_UNSIGNED_SNAPSHOT=\"yes\" in vault.cfg and restart")
	}
	return envErr
}

// SavedAt returns a candidate's signed save time, if it has a valid envelope.
func (a *SnapshotAuth) SavedAt(path string) (int64, bool) {
	env, err := a.read(path)
	return env.SavedAt, err == nil
}

// VerifiedCopy copies a candidate to a private directory and checks the copy
// against the envelope. A WAL beside the snapshot is deliberately left behind: the
// envelope does not cover one and Vault's snapshots never have one. A candidate
// Admissible let through without an envelope is returned in place.
func (a *SnapshotAuth) VerifiedCopy(path string) (string, func(), error) {
	noop := func() {}
	// One read decides: re-reading the envelope after a failure would let an
	// envelope put back in between skip the content check below.
	env, err := a.read(path)
	if err != nil {
		if admitErr := a.unsignedDecision(path, err); admitErr != nil {
			return "", noop, admitErr
		}
		return path, noop, nil
	}
	in, err := openRegular(path)
	if err != nil {
		return "", noop, err
	}
	defer in.Close()
	dir, err := os.MkdirTemp("", "vault-snapshot-verify-*")
	if err != nil {
		return "", noop, fmt.Errorf("creating private copy: %w", err)
	}
	release := func() { _ = os.RemoveAll(dir) }
	copyPath := filepath.Join(dir, "snapshot.db")
	out, err := os.OpenFile(copyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- inside a directory this process just created
	if err != nil {
		release()
		return "", noop, err
	}
	sum := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, sum), in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		release()
		return "", noop, fmt.Errorf("copying snapshot: %w", err)
	}
	if !hmac.Equal([]byte(hex.EncodeToString(sum.Sum(nil))), []byte(env.SHA256)) {
		release()
		return "", noop, errors.New("contents do not match the authentication envelope")
	}
	return copyPath, release, nil
}

// openRegular opens a file only if it and the opened handle are regular files,
// refusing a symlink at the final path component.
func openRegular(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || strings.Contains(path, "..") {
		return nil, fmt.Errorf("refusing suspicious snapshot path %q", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path) // #nosec G304 -- snapshot paths are configured by the operator and checked above
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, fmt.Errorf("%s changed while opening", path)
	}
	return f, nil
}

func hashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeFileAtomic(path string, data []byte) error {
	if !filepath.IsAbs(path) || strings.Contains(path, "..") {
		return fmt.Errorf("refusing suspicious snapshot path %q", path)
	}
	dir, name := filepath.Dir(path), filepath.Base(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// The snapshot tree may be writable by others: a directory swapped for a
	// symlink must not send a root-owned write elsewhere. Everything below goes
	// through a handle on the real directory.
	if info, err := os.Lstat(dir); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tmpName := fmt.Sprintf("%s.tmp-%d", name, time.Now().UnixNano())
	tmp, err := root.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = syncFile(tmp)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(tmpName, name)
	}
	if err != nil {
		_ = root.Remove(tmpName)
		return err
	}
	// The rename is durable only once the directory entry is on disk.
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	err = syncFile(d)
	if closeErr := d.Close(); err == nil {
		err = closeErr
	}
	return err
}

// syncFile is (*os.File).Sync, swappable so tests can make it fail.
var syncFile = (*os.File).Sync

// SetSnapshotAuth turns on signing and lets restore require envelopes.
func (sm *SnapshotManager) SetSnapshotAuth(a *SnapshotAuth) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.auth = a
}

// SnapshotAuth returns the configured authenticator, or nil.
func (sm *SnapshotManager) SnapshotAuth() *SnapshotAuth {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.auth
}

// sealSigned seals a signed copy. It reports false, leaving the caller to seal
// in place, when no authenticator is set or the path has no envelope. Otherwise
// the copy is verified into a private WAL-free file, sealed there, written back
// in place of the original (whose WAL and SHM are dropped, keeping its mtime) and
// signed. A copy that fails verification is left untouched and reported as an
// error, so the scrub stays pending and is retried at the next start. One whose
// envelope the unsigned-snapshot policy waives is handed back as unsigned.
func (sm *SnapshotManager) sealSigned(path string, key []byte) (bool, error) {
	auth := sm.SnapshotAuth()
	if auth == nil {
		return false, nil
	}
	if _, err := os.Lstat(envelopePath(path)); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return true, fmt.Errorf("reading envelope: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return true, err
	}
	private, release, err := auth.VerifiedCopy(path)
	defer release()
	if err != nil {
		return true, fmt.Errorf("not sealed, it does not match its envelope: %w", err)
	}
	if private == path {
		return false, nil
	}
	if err := SealCopy(private, key); err != nil {
		return true, err
	}
	sealed, err := os.ReadFile(private) // #nosec G304 -- private copy in a directory this process created
	if err != nil {
		return true, err
	}
	if err := writeFileAtomic(path, sealed); err != nil {
		return true, err
	}
	_ = removeValidated(path + "-wal")
	_ = removeValidated(path + "-shm")
	if err := os.Chtimes(path, time.Now(), info.ModTime()); err != nil {
		return true, err
	}
	return true, auth.Sign(path)
}

// CarryAPIKey copies the API key settings in force on from onto to, so replacing
// the database from a backup file cannot change who may use the API. A server
// with no key configured has nothing to protect and leaves to as it is.
func CarryAPIKey(from, to *DB) error {
	hash, err := from.GetSetting("api_key_hash", "")
	if err != nil {
		return err
	}
	if hash == "" {
		return nil
	}
	sealed, err := from.GetSetting("api_key_sealed", "")
	if err != nil {
		return err
	}
	if err := to.SetSetting("api_key_hash", hash); err != nil {
		return err
	}
	return to.SetSetting("api_key_sealed", sealed)
}

// CarryAssistantKeys replaces the candidate key set with the live key set,
// preserving IDs and their allocation high-water mark across a database swap.
func CarryAssistantKeys(ctx context.Context, from, to *DB) error {
	type credential struct {
		AssistantKey
		digest string
	}
	read := func() ([]credential, error) {
		rows, err := from.QueryContext(ctx, `SELECT id, name, key_digest, created_at, restore_request_grant FROM assistant_keys`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var keys []credential
		for rows.Next() {
			var key credential
			if err := rows.Scan(&key.ID, &key.Name, &key.digest, &key.CreatedAt, &key.RestoreGrant); err != nil {
				return nil, err
			}
			keys = append(keys, key)
		}
		return keys, rows.Err()
	}
	keys, err := read()
	if err != nil {
		return err
	}

	var highWater int64
	if err := from.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM sqlite_sequence WHERE name = 'assistant_keys'`).Scan(&highWater); err != nil {
		return err
	}
	tx, err := to.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_keys`); err != nil {
		return err
	}
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_keys (id, name, key_digest, created_at, restore_request_grant) VALUES (?, ?, ?, ?, ?)`,
			key.ID, key.Name, key.digest, key.CreatedAt, key.RestoreGrant); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sqlite_sequence (name, seq)
        SELECT 'assistant_keys', 0 WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'assistant_keys')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = MAX(seq, ?) WHERE name = 'assistant_keys'`, highWater); err != nil {
		return err
	}
	return tx.Commit()
}

package dedup

// Passphrase escrow for the repository master key (issue #451).
//
// The master key in repo.json is sealed with the server's vault.key, which
// lives only on the server. Losing that file used to make every dedup
// destination unreadable. The escrow is a second copy of the same master key,
// sealed with the backup passphrase using age's scrypt mode, so the backup
// password alone can unlock a destination.
//
// It lives in its own file, not in repo.json. Routine backups create and
// refresh it, and they must never rewrite the one file whose loss would make
// the destination unreadable — storage adapters cannot rename atomically.
// repo.json is rewritten only by RewrapMaster, an explicit recovery step, and
// only after a verbatim backup copy has been written.

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// escrowPath holds the passphrase-sealed copy of the master key.
const escrowPath = RepoRoot + "/master.escrow.age"

// escrowVersion is the payload format inside the age envelope.
const escrowVersion = 1

// KeyMismatchHint is the user-facing explanation for ErrServerKeyMismatch.
const KeyMismatchHint = "This dedup destination was sealed with a different vault.key. Restore the original vault.key, or restore the database from the Recovery wizard with your backup password."

var (
	// ErrServerKeyMismatch means the supplied server key cannot unseal the
	// repository's master key: the destination was created with another
	// vault.key (or its header is damaged).
	ErrServerKeyMismatch = errors.New("dedup: server key does not match this repository")
	// ErrNoPassphraseEscrow means the repository has no passphrase escrow,
	// because no backup has run with a backup passphrase configured.
	ErrNoPassphraseEscrow = errors.New("dedup: repository has no backup-passphrase escrow")
	// ErrPassphraseMismatch means the passphrase does not open the escrow.
	ErrPassphraseMismatch = errors.New("dedup: backup passphrase does not open this repository's escrow")
)

// escrowPayload is what the age envelope protects. The repository UUID binds
// an escrow to its own repository: age authenticates the payload, so a copy
// moved from another destination is rejected rather than trusted.
type escrowPayload struct {
	Version  int    `json:"version"`
	RepoUUID string `json:"repo_uuid"`
	Master   []byte `json:"master"`
}

// readEscrow returns the master key held in the escrow. It fails with
// ErrNoPassphraseEscrow when there is none and ErrPassphraseMismatch when
// passphrase does not open it (or it belongs to another repository).
func readEscrow(a storage.Adapter, repoUUID, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, ErrNoPassphraseEscrow
	}
	rc, err := a.Read(escrowPath)
	if err != nil {
		if storage.IsNotExist(err) {
			return nil, ErrNoPassphraseEscrow
		}
		return nil, fmt.Errorf("dedup: read escrow: %w", err)
	}
	defer rc.Close()
	plain, err := crypto.DecryptReader(passphrase, rc)
	if err != nil {
		if crypto.IsWrongPassphrase(err) {
			return nil, ErrPassphraseMismatch
		}
		return nil, fmt.Errorf("dedup: open escrow: %w", err)
	}
	defer plain.Close()
	var p escrowPayload
	if err := json.NewDecoder(plain).Decode(&p); err != nil {
		return nil, fmt.Errorf("dedup: decode escrow: %w", err)
	}
	if p.Version != escrowVersion || p.RepoUUID != repoUUID || len(p.Master) != SecretSize {
		return nil, fmt.Errorf("%w: escrow belongs to another repository", ErrPassphraseMismatch)
	}
	return p.Master, nil
}

// EnsurePassphraseEscrow makes sure the repository's escrow opens with
// passphrase, writing a fresh one when it is missing, sealed with another
// passphrase, or stale. It reports whether it wrote. One check costs one
// scrypt derivation (about a second), so callers should not repeat it for
// a passphrase they have already confirmed.
func (r *Repo) EnsurePassphraseEscrow(passphrase string) (bool, error) {
	if passphrase == "" {
		return false, errors.New("dedup: escrow needs a non-empty passphrase")
	}
	if master, err := readEscrow(r.adapter, r.uuid, passphrase); err == nil && subtle.ConstantTimeCompare(master, r.master) == 1 {
		return false, nil
	} else if err != nil && !errors.Is(err, ErrNoPassphraseEscrow) && !errors.Is(err, ErrPassphraseMismatch) {
		return false, err
	}
	body, err := json.Marshal(escrowPayload{Version: escrowVersion, RepoUUID: r.uuid, Master: r.master})
	if err != nil {
		return false, err
	}
	enc, err := crypto.EncryptReader(passphrase, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	sealed, err := io.ReadAll(enc)
	_ = enc.Close()
	if err != nil {
		return false, fmt.Errorf("dedup: seal escrow: %w", err)
	}
	if err := r.adapter.Write(escrowPath, bytes.NewReader(sealed)); err != nil {
		return false, fmt.Errorf("dedup: write escrow: %w", err)
	}
	return true, nil
}

// OpenRepoFromEscrow opens a repository through its passphrase escrow
// instead of the server key. Nothing is written: use it to read when
// vault.key is gone, and RewrapMaster to fix the repository for good.
func OpenRepoFromEscrow(d *db.DB, a storage.Adapter, storageID int64, passphrase string) (*Repo, error) {
	cfg, _, err := readRepoConfig(a)
	if err != nil {
		return nil, err
	}
	master, err := readEscrow(a, cfg.UUID, passphrase)
	if err != nil {
		return nil, err
	}
	r := buildRepo(d, a, storageID, master, cfg.UUID)
	r.viaEscrow = true
	return r, nil
}

// UnlockedByPassphrase reports whether the repository was opened through the
// passphrase escrow, meaning the server key in use does not match it. The
// data is fully usable, but the destination stays mis-keyed until the
// original vault.key is restored or RewrapMaster runs.
func (r *Repo) UnlockedByPassphrase() bool { return r.viaEscrow }

// RewrapMaster re-seals the repository's master key with serverKey after
// recovering it through the passphrase escrow — the step that makes a
// restored server, which has a new vault.key, able to open the repository
// on its own again. The current header is first copied verbatim to
// _vault/repo.json.<time>.bak; the new header is read back and must open
// with serverKey. Data, packs and the escrow are untouched.
func RewrapMaster(a storage.Adapter, serverKey []byte, passphrase string, now time.Time) error {
	cfg, original, err := readRepoConfig(a)
	if err != nil {
		return err
	}
	master, err := readEscrow(a, cfg.UUID, passphrase)
	if err != nil {
		return err
	}
	sealed, err := SealMaster(serverKey, master)
	if err != nil {
		return err
	}
	backup := fmt.Sprintf("%s.%s.bak", repoConfigPath, now.UTC().Format("20060102T150405Z"))
	if err := a.Write(backup, bytes.NewReader(original)); err != nil {
		return fmt.Errorf("dedup: back up repo.json before rewrap: %w", err)
	}
	cfg.SealedMaster = sealed
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := a.Write(repoConfigPath, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("dedup: write rewrapped repo.json (original kept at %s): %w", backup, err)
	}
	check, _, err := readRepoConfig(a)
	if err == nil {
		var got []byte
		if got, err = UnsealMaster(serverKey, check.SealedMaster); err == nil && subtle.ConstantTimeCompare(got, master) != 1 {
			err = errors.New("unsealed a different key")
		}
	}
	if err != nil {
		return fmt.Errorf("dedup: rewrapped repo.json did not verify (original kept at %s): %w", backup, err)
	}
	return nil
}

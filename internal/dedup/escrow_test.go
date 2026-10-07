package dedup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func readFile(t *testing.T, r *Repo, p string) []byte {
	t.Helper()
	rc, err := r.adapter.Read(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return b
}

// TestEscrowRoundTrip checks the backup passphrase alone opens a repository
// once an escrow exists, and that writing it never touches repo.json.
func TestEscrowRoundTrip(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	header := readFile(t, r, repoConfigPath)
	id, err := r.Put([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenRepoFromEscrow(r.db, r.adapter, r.storageID, "pw"); !errors.Is(err, ErrNoPassphraseEscrow) {
		t.Fatalf("before escrow: %v, want ErrNoPassphraseEscrow", err)
	}
	wrote, err := r.EnsurePassphraseEscrow("pw")
	if err != nil || !wrote {
		t.Fatalf("EnsurePassphraseEscrow = %v, %v", wrote, err)
	}
	if !bytes.Equal(readFile(t, r, repoConfigPath), header) {
		t.Fatal("writing the escrow changed repo.json")
	}
	if wrote, err := r.EnsurePassphraseEscrow("pw"); err != nil || wrote {
		t.Fatalf("second EnsurePassphraseEscrow = %v, %v; want no write", wrote, err)
	}

	opened, err := OpenRepoFromEscrow(r.db, r.adapter, r.storageID, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !opened.UnlockedByPassphrase() || r.UnlockedByPassphrase() {
		t.Fatalf("UnlockedByPassphrase: escrow=%v, server key=%v", opened.UnlockedByPassphrase(), r.UnlockedByPassphrase())
	}
	if got, err := opened.Get(id); err != nil || string(got) != "hello" {
		t.Fatalf("Get through escrow = %q, %v", got, err)
	}
	if _, err := OpenRepoFromEscrow(r.db, r.adapter, r.storageID, "wrong"); !errors.Is(err, ErrPassphraseMismatch) {
		t.Fatalf("wrong passphrase: %v, want ErrPassphraseMismatch", err)
	}
	if _, err := OpenRepoFromEscrow(r.db, r.adapter, r.storageID, ""); !errors.Is(err, ErrNoPassphraseEscrow) {
		t.Fatalf("empty passphrase: %v", err)
	}
	if _, err := r.EnsurePassphraseEscrow(""); err == nil {
		t.Fatal("empty passphrase accepted for escrow")
	}

	// A passphrase change replaces the escrow; the old one stops working.
	if wrote, err := r.EnsurePassphraseEscrow("new"); err != nil || !wrote {
		t.Fatalf("passphrase change = %v, %v", wrote, err)
	}
	if _, err := OpenRepoFromEscrow(r.db, r.adapter, r.storageID, "pw"); !errors.Is(err, ErrPassphraseMismatch) {
		t.Fatalf("old passphrase after change: %v", err)
	}
}

// TestEscrowFromAnotherRepositoryIsRejected checks an escrow copied between
// destinations does not unlock the wrong one.
func TestEscrowFromAnotherRepositoryIsRejected(t *testing.T) {
	a, _, cleanA := newTestRepo(t)
	defer cleanA()
	b, _, cleanB := newTestRepo(t)
	defer cleanB()
	if _, err := a.EnsurePassphraseEscrow("pw"); err != nil {
		t.Fatal(err)
	}
	if err := b.adapter.Write(escrowPath, bytes.NewReader(readFile(t, a, escrowPath))); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRepoFromEscrow(b.db, b.adapter, b.storageID, "pw"); !errors.Is(err, ErrPassphraseMismatch) {
		t.Fatalf("foreign escrow: %v, want ErrPassphraseMismatch", err)
	}
	// Ensure replaces the foreign copy with this repository's own.
	if wrote, err := b.EnsurePassphraseEscrow("pw"); err != nil || !wrote {
		t.Fatalf("Ensure over a foreign escrow = %v, %v", wrote, err)
	}
	if _, err := OpenRepoFromEscrow(b.db, b.adapter, b.storageID, "pw"); err != nil {
		t.Fatalf("own escrow after replace: %v", err)
	}
}

// TestRewrapMasterAfterServerKeyLoss is the #451 recovery: a new server key
// cannot open the repository, the escrow recovers the master, and after the
// rewrap the new key opens it directly. The old header is kept.
func TestRewrapMasterAfterServerKeyLoss(t *testing.T) {
	r, oldKey, cleanup := newTestRepo(t)
	defer cleanup()
	id, _ := r.Put([]byte("survives"))
	_ = r.Flush()
	if _, err := r.EnsurePassphraseEscrow("pw"); err != nil {
		t.Fatal(err)
	}
	newKey := bytes.Repeat([]byte{0x11}, SecretSize)

	_, err := OpenRepo(r.db, r.adapter, r.storageID, newKey)
	if !errors.Is(err, ErrServerKeyMismatch) || !strings.Contains(err.Error(), "wrong serverKey?") {
		t.Fatalf("new key before rewrap: %v, want ErrServerKeyMismatch with the old message", err)
	}
	if err := RewrapMaster(r.adapter, newKey, "wrong", time.Now()); !errors.Is(err, ErrPassphraseMismatch) {
		t.Fatalf("rewrap with wrong passphrase: %v", err)
	}

	now := time.Date(2026, 10, 7, 3, 4, 5, 0, time.UTC)
	if err := RewrapMaster(r.adapter, newKey, "pw", now); err != nil {
		t.Fatalf("RewrapMaster: %v", err)
	}
	opened, err := OpenRepo(r.db, r.adapter, r.storageID, newKey)
	if err != nil {
		t.Fatalf("new key after rewrap: %v", err)
	}
	if got, err := opened.Get(id); err != nil || string(got) != "survives" {
		t.Fatalf("data after rewrap = %q, %v", got, err)
	}
	if _, err := OpenRepo(r.db, r.adapter, r.storageID, oldKey); !errors.Is(err, ErrServerKeyMismatch) {
		t.Fatalf("old key after rewrap: %v", err)
	}
	backup := readFile(t, r, repoConfigPath+".20261007T030405Z.bak")
	var cfg repoConfig
	if err := json.Unmarshal(backup, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := UnsealMaster(oldKey, cfg.SealedMaster); err != nil {
		t.Fatalf("backup header is not the original: %v", err)
	}
	// The escrow keeps working after the rewrap.
	if _, err := OpenRepoFromEscrow(r.db, r.adapter, r.storageID, "pw"); err != nil {
		t.Fatalf("escrow after rewrap: %v", err)
	}
}

// TestRewrapWithoutEscrowFails checks a repository that never had an escrow
// reports that, and is left untouched.
func TestRewrapWithoutEscrowFails(t *testing.T) {
	r, oldKey, cleanup := newTestRepo(t)
	defer cleanup()
	before := readFile(t, r, repoConfigPath)
	if err := RewrapMaster(r.adapter, bytes.Repeat([]byte{0x11}, SecretSize), "pw", time.Now()); !errors.Is(err, ErrNoPassphraseEscrow) {
		t.Fatalf("rewrap without escrow: %v", err)
	}
	if !bytes.Equal(readFile(t, r, repoConfigPath), before) {
		t.Fatal("repo.json changed although the rewrap failed")
	}
	if _, err := OpenRepo(r.db, r.adapter, r.storageID, oldKey); err != nil {
		t.Fatal(err)
	}
}

// TestOpenRepoUnsupportedVersionValidHeader reaches the version check with
// an otherwise valid header (the older fixture fails earlier, on base64).
func TestOpenRepoUnsupportedVersionValidHeader(t *testing.T) {
	r, key, cleanup := newTestRepo(t)
	defer cleanup()
	var cfg repoConfig
	_ = json.Unmarshal(readFile(t, r, repoConfigPath), &cfg)
	cfg.Version = 2
	body, _ := json.Marshal(cfg)
	_ = r.adapter.Write(repoConfigPath, bytes.NewReader(body))
	if _, err := OpenRepo(r.db, r.adapter, r.storageID, key); err == nil || !strings.Contains(err.Error(), "unsupported repo version 2") {
		t.Fatalf("OpenRepo = %v, want the unsupported-version error", err)
	}
}

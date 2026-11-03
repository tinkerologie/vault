package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSnapshot(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func testAuth(t *testing.T, allowUnsigned bool) (*SnapshotAuth, string) {
	t.Helper()
	anchor := t.TempDir()
	return NewSnapshotAuth(make([]byte, 32), anchor, allowUnsigned), anchor
}

func TestSnapshotAuthRoundTrip(t *testing.T) {
	a, _ := testAuth(t, false)
	snap := writeSnapshot(t, t.TempDir(), "vault.db", "payload")
	if err := a.Sign(snap); err != nil {
		t.Fatal(err)
	}
	if err := a.Admissible(snap); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(snap)
	if saved, ok := a.SavedAt(snap); !ok || saved != info.ModTime().UnixNano() {
		t.Errorf("saved = %d, ok = %v", saved, ok)
	}
	copyPath, release, err := a.VerifiedCopy(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if copyPath == snap {
		t.Error("restored in place, not from the checked copy")
	}
	if got, _ := os.ReadFile(copyPath); string(got) != "payload" {
		t.Errorf("copy = %q", got)
	}
}

func TestSnapshotAuthRefusals(t *testing.T) {
	a, _ := testAuth(t, false)
	dir := t.TempDir()
	signed := writeSnapshot(t, dir, "signed.db", "payload")
	if err := a.Sign(signed); err != nil { // also enrolls
		t.Fatal(err)
	}

	t.Run("replaced contents", func(t *testing.T) {
		snap := writeSnapshot(t, dir, "tampered.db", "good")
		if err := a.Sign(snap); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(snap, []byte("evil"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := a.VerifiedCopy(snap); err == nil || !strings.Contains(err.Error(), "do not match") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("removed envelope", func(t *testing.T) {
		snap := writeSnapshot(t, dir, "bare.db", "x")
		err := a.Admissible(snap)
		if err == nil || !strings.Contains(err.Error(), "ALLOW_UNSIGNED_SNAPSHOT") {
			t.Fatalf("err = %v", err)
		}
		if _, _, err := a.VerifiedCopy(snap); err == nil {
			t.Fatal("want refusal")
		}
	})
	t.Run("forged envelope", func(t *testing.T) {
		snap := writeSnapshot(t, dir, "forged.db", "x")
		other := NewSnapshotAuth([]byte(strings.Repeat("k", 32)), t.TempDir(), false)
		if err := other.Sign(snap); err != nil {
			t.Fatal(err)
		}
		if err := a.Admissible(snap); err == nil || !strings.Contains(err.Error(), "does not match this server") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("garbage and oversized envelopes", func(t *testing.T) {
		for name, body := range map[string]string{"garbage": "{", "big": strings.Repeat("a", snapshotEnvelopeMax+1)} {
			snap := writeSnapshot(t, dir, name+".db", "x")
			writeSnapshot(t, dir, name+".db"+snapshotEnvelopeSuffix, body)
			if err := a.Admissible(snap); err == nil {
				t.Errorf("%s: want refusal", name)
			}
		}
	})
	t.Run("symlinked snapshot", func(t *testing.T) {
		link := filepath.Join(dir, "link.db")
		if err := os.Symlink(signed, link); err != nil {
			t.Fatal(err)
		}
		if err := a.Admissible(link); err == nil {
			t.Fatal("want refusal")
		}
		if _, err := openRegular(link); err == nil {
			t.Fatal("want refusal")
		}
	})
	t.Run("missing snapshot", func(t *testing.T) {
		if err := a.Admissible(filepath.Join(dir, "none.db")); err == nil {
			t.Fatal("want error")
		}
		if err := a.Sign(filepath.Join(dir, "none.db")); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestSnapshotAuthExistingSnapshotsStayRestorable(t *testing.T) {
	t.Run("before this server has ever signed", func(t *testing.T) {
		a, _ := testAuth(t, false)
		snap := writeSnapshot(t, t.TempDir(), "old.db", "x")
		if err := a.Admissible(snap); err != nil {
			t.Fatal(err)
		}
		got, release, err := a.VerifiedCopy(snap)
		if err != nil || got != snap {
			t.Fatalf("got %q, %v", got, err)
		}
		release()
	})
	t.Run("a replacement flash drive's new key does not reject them", func(t *testing.T) {
		snap := writeSnapshot(t, t.TempDir(), "vault.db", "x")
		if err := NewSnapshotAuth([]byte(strings.Repeat("o", 32)), t.TempDir(), false).Sign(snap); err != nil {
			t.Fatal(err)
		}
		a, _ := testAuth(t, false)
		if err := a.Admissible(snap); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the operator override after enrollment", func(t *testing.T) {
		a, _ := testAuth(t, true)
		dir := t.TempDir()
		if err := a.Sign(writeSnapshot(t, dir, "new.db", "x")); err != nil {
			t.Fatal(err)
		}
		if err := a.Admissible(writeSnapshot(t, dir, "old.db", "x")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestVerifiedCopyLeavesAPlantedWALBehind(t *testing.T) {
	a, _ := testAuth(t, false)
	dir := t.TempDir()
	snap := writeSnapshot(t, dir, "vault.db", "x")
	if err := a.Sign(snap); err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, dir, "vault.db-wal", "evil")
	copyPath, release, err := a.VerifiedCopy(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := os.Stat(copyPath + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("WAL came along: %v", err)
	}
}

func TestSnapshotManagerSignsSavesAndRotations(t *testing.T) {
	d := setupTestDB(t)
	dir := t.TempDir()
	snap := filepath.Join(dir, "vault.db")
	a, _ := testAuth(t, false)
	sm := NewSnapshotManager(d, snap, snap)
	sm.SetSnapshotAuth(a)
	for range rotatedSnapshotsKept + 2 {
		if err := sm.SaveSnapshot(); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Admissible(snap); err != nil {
		t.Fatal(err)
	}
	rotated, err := os.ReadDir(filepath.Join(dir, "rotated"))
	if err != nil {
		t.Fatal(err)
	}
	envelopes, err := os.ReadDir(filepath.Join(dir, rotatedEnvelopeDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(rotated) != rotatedSnapshotsKept || len(envelopes) != len(rotated) {
		t.Fatalf("rotated = %d, envelopes = %d", len(rotated), len(envelopes))
	}
	for _, e := range rotated {
		if strings.HasSuffix(e.Name(), snapshotEnvelopeSuffix) {
			t.Errorf("envelope %s would be read as a database copy", e.Name())
		}
		p := filepath.Join(dir, "rotated", e.Name())
		if _, release, err := a.VerifiedCopy(p); err != nil {
			t.Errorf("rotated copy: %v", err)
		} else {
			release()
		}
	}
	if handled, err := sm.sealSigned(filepath.Join(dir, "never-signed.db"), credKeyA); handled || err != nil {
		t.Errorf("unsigned path: handled = %v, err = %v", handled, err)
	}
	if handled, err := (&SnapshotManager{}).sealSigned(snap, credKeyA); handled || err != nil {
		t.Errorf("no auth configured: handled = %v, err = %v", handled, err)
	}
}

func TestCarryAPIKey(t *testing.T) {
	current, candidate := setupTestDB(t), setupTestDB(t)
	if err := candidate.SetSetting("api_key_hash", ""); err != nil {
		t.Fatal(err)
	}
	if err := CarryAPIKey(current, candidate); err != nil {
		t.Fatal(err)
	}
	if got, _ := candidate.GetSetting("api_key_hash", ""); got != "" {
		t.Errorf("no key to carry, got %q", got)
	}
	if err := current.SetSetting("api_key_hash", "h"); err != nil {
		t.Fatal(err)
	}
	if err := current.SetSetting("api_key_sealed", "s"); err != nil {
		t.Fatal(err)
	}
	if err := CarryAPIKey(current, candidate); err != nil {
		t.Fatal(err)
	}
	if h, _ := candidate.GetSetting("api_key_hash", ""); h != "h" {
		t.Errorf("hash = %q", h)
	}
	if s, _ := candidate.GetSetting("api_key_sealed", ""); s != "s" {
		t.Errorf("sealed = %q", s)
	}
}

func TestSnapshotAuthFailurePaths(t *testing.T) {
	dir := t.TempDir()
	real := writeSnapshot(t, dir, "real.db", "x")
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	t.Run("sign refuses a symlink", func(t *testing.T) {
		a, _ := testAuth(t, false)
		if err := a.Sign(link); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("sign reports an unwritable trust anchor", func(t *testing.T) {
		anchorFile := writeSnapshot(t, t.TempDir(), "not-a-dir", "")
		a := NewSnapshotAuth(make([]byte, 32), anchorFile, false)
		if err := a.Sign(writeSnapshot(t, t.TempDir(), "s.db", "x")); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("sign reports an unwritable envelope location", func(t *testing.T) {
		a, _ := testAuth(t, false)
		d := t.TempDir()
		snap := writeSnapshot(t, d, "s.db", "x")
		if err := os.Mkdir(snap+snapshotEnvelopeSuffix, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := a.Sign(snap); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("copying a missing envelope fails", func(t *testing.T) {
		a, _ := testAuth(t, false)
		if err := a.copyEnvelope(real, filepath.Join(dir, "rotated", "x")); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("a signed snapshot replaced by a symlink is refused", func(t *testing.T) {
		a, _ := testAuth(t, false)
		d := t.TempDir()
		snap := writeSnapshot(t, d, "s.db", "x")
		if err := a.Sign(snap); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(snap); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, snap); err != nil {
			t.Fatal(err)
		}
		if _, _, err := a.VerifiedCopy(snap); err == nil {
			t.Fatal("want refusal")
		}
	})
	t.Run("no temp directory", func(t *testing.T) {
		a, _ := testAuth(t, false)
		snap := writeSnapshot(t, t.TempDir(), "s.db", "x")
		if err := a.Sign(snap); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		if _, _, err := a.VerifiedCopy(snap); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("carrying the key from or to a closed database fails", func(t *testing.T) {
		open, closed := setupTestDB(t), setupTestDB(t)
		_ = closed.Close()
		if err := CarryAPIKey(closed, open); err == nil {
			t.Error("closed source: want error")
		}
		if err := open.SetSetting("api_key_hash", "h"); err != nil {
			t.Fatal(err)
		}
		if err := CarryAPIKey(open, closed); err == nil {
			t.Error("closed destination: want error")
		}
	})
	t.Run("save fails when the envelope cannot be written", func(t *testing.T) {
		d := setupTestDB(t)
		snapDir := t.TempDir()
		snap := filepath.Join(snapDir, "vault.db")
		if err := os.Mkdir(snap+snapshotEnvelopeSuffix, 0o750); err != nil {
			t.Fatal(err)
		}
		sm := NewSnapshotManager(d, snap, snap)
		a, _ := testAuth(t, false)
		sm.SetSnapshotAuth(a)
		if err := sm.SaveSnapshot(); err == nil || !strings.Contains(err.Error(), "authenticate snapshot") {
			t.Fatalf("err = %v", err)
		}
	})
}

// Sealing rewrites copies and re-signs them, so a signed copy someone replaced
// must not be sealed into a validly signed one.
func TestSealPersistentCopiesSkipsReplacedSignedCopy(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	seedPlaintext(t, d)
	snap := filepath.Join(dir, "snap", "vault.db")
	sm := NewSnapshotManager(d, snap, snap)
	a, _ := testAuth(t, false)
	sm.SetSnapshotAuth(a)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatal(err)
	}
	rotated, _ := filepath.Glob(filepath.Join(dir, "snap", "rotated", "*"))
	if len(rotated) != 1 {
		t.Fatalf("rotated = %v", rotated)
	}
	// Replace the rotated copy (it is hard-linked to the primary, so unlink first).
	if err := os.Remove(rotated[0]); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(filepath.Join(dir, "vault.db"), rotated[0]); err != nil {
		t.Fatal(err)
	}
	d.SetCredentialKey(credKeyA)
	if _, err := d.SealSecretsAtRest(); err != nil {
		t.Fatal(err)
	}
	if err := d.SealPersistentCopies(context.Background(), sm, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.VerifiedCopy(rotated[0]); err == nil {
		t.Fatal("replaced copy was re-signed")
	}
	if _, release, err := a.VerifiedCopy(snap); err != nil {
		t.Fatalf("primary: %v", err)
	} else {
		release()
	}
}

func TestUnreadableEnvelopeIsNotTreatedAsUnsigned(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	a, _ := testAuth(t, false)
	d := setupTestDB(t)
	sm := NewSnapshotManager(d, filepath.Join(t.TempDir(), "x.db"), "")
	sm.SetSnapshotAuth(a)
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	snap := writeSnapshot(t, locked, "vault.db", "x")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o750) })
	if handled, err := sm.sealSigned(snap, credKeyA); !handled || err == nil {
		t.Errorf("handled = %v, err = %v; want the copy left alone with an error", handled, err)
	}
}

// Changes still only in the working database's WAL must be inside the signed
// snapshot, because the verified copy never carries a WAL across.
func TestSignedSnapshotIncludesPendingWALChanges(t *testing.T) {
	dir := t.TempDir()
	working, err := Open(filepath.Join(dir, "working.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = working.Close() })
	if _, err := working.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if err := working.SetSetting("wal_only", "committed"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "working.db-wal")); err != nil || info.Size() == 0 {
		t.Fatalf("test premise: change should be pending in the WAL (%v)", err)
	}
	snap := filepath.Join(dir, "cache", "vault.db")
	a, _ := testAuth(t, false)
	sm := NewSnapshotManager(working, snap, snap)
	sm.SetSnapshotAuth(a)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	restorer := NewSnapshotManager(fresh, snap, snap)
	restorer.SetSnapshotAuth(a)
	copyPath, release, err := a.VerifiedCopy(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := restorer.RestoreFromPath(copyPath); err != nil {
		t.Fatal(err)
	}
	if got, _ := fresh.GetSetting("wal_only", ""); got != "committed" {
		t.Fatalf("restored setting = %q", got)
	}
}

// A WAL planted beside a signed copy must not be replayed into the copy that
// sealing re-signs (Codex review of #592).
func TestSealSignedIgnoresPlantedWAL(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.SetSetting("api_key_hash", "genuine"); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(dir, "snap", "vault.db")
	sm := NewSnapshotManager(d, snap, snap)
	a, _ := testAuth(t, false)
	sm.SetSnapshotAuth(a)
	if err := sm.SaveSnapshot(); err != nil {
		t.Fatal(err)
	}

	// Build a WAL that rewrites api_key_hash against a byte-identical copy of
	// the snapshot, then plant it beside the signed snapshot.
	forge := filepath.Join(t.TempDir(), "vault.db")
	if err := copyFile(snap, forge); err != nil {
		t.Fatal(err)
	}
	fd, err := Open(forge)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fd.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if err := fd.SetSetting("api_key_hash", "attacker"); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(forge+"-wal", snap+"-wal"); err != nil {
		t.Fatal(err)
	}
	_ = fd.Close()

	if handled, err := sm.sealSigned(snap, credKeyA); !handled || err != nil {
		t.Fatalf("handled = %v, err = %v", handled, err)
	}
	copyPath, release, err := a.VerifiedCopy(snap)
	if err != nil {
		t.Fatalf("sealed snapshot no longer verifies: %v", err)
	}
	defer release()
	check, err := Open(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	if got, _ := check.GetSetting("api_key_hash", ""); got != "genuine" {
		t.Fatalf("signed snapshot carries api_key_hash %q", got)
	}
	if _, err := os.Stat(snap + "-wal"); !os.IsNotExist(err) {
		t.Errorf("planted WAL still beside the snapshot: %v", err)
	}
}

// An envelope removed for the first read and put back before any second read
// must not let the restore skip the content check (Greptile review of #592).
func TestVerifiedCopyDecidesOnOneEnvelopeRead(t *testing.T) {
	a, _ := testAuth(t, false)
	dir := t.TempDir()
	if err := a.Sign(writeSnapshot(t, dir, "enroll.db", "x")); err != nil {
		t.Fatal(err)
	}
	snap := writeSnapshot(t, dir, "vault.db", "good")
	if err := a.Sign(snap); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snap, []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	reads := 0
	a.readHook = func(p string) (snapshotEnvelope, error) {
		reads++
		if reads == 1 {
			return snapshotEnvelope{}, os.ErrNotExist
		}
		return a.readEnvelope(p)
	}
	got, release, err := a.VerifiedCopy(snap)
	release()
	if err == nil {
		t.Fatalf("restored %q without checking its contents", got)
	}
}

func TestWriteFileAtomicDurabilityAndSymlinks(t *testing.T) {
	t.Run("a sync failure is reported", func(t *testing.T) {
		old := syncFile
		syncFile = func(*os.File) error { return errors.New("disk gone") }
		t.Cleanup(func() { syncFile = old })
		a, _ := testAuth(t, false)
		if err := a.Sign(writeSnapshot(t, t.TempDir(), "s.db", "x")); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("a symlinked envelope directory is refused", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dir, rotatedEnvelopeDir)); err != nil {
			t.Skip(err)
		}
		if err := writeFileAtomic(filepath.Join(dir, rotatedEnvelopeDir, "x.auth"), []byte("{}")); err == nil {
			t.Fatal("want refusal")
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Errorf("wrote through the symlink: %v", entries)
		}
	})
}

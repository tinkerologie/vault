package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// TestNewestRotatedSnapshots pins the rotated-tier lookup: global newest-first
// ordering by modification time across multiple rotated dirs (basenames with
// different prefixes do NOT sort chronologically), a cap of 3, and dedup of
// shared directories.
func TestNewestRotatedSnapshots(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	rotA := filepath.Join(dirA, "rotated")
	rotB := filepath.Join(dirB, "rotated")
	for _, d := range []string{rotA, rotB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Now().Add(-time.Hour)
	write := func(dir, name string, age time.Duration) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Basenames deliberately chosen so lexical order CONTRADICTS mtime order
	// across directories ("a-..." vs "z-...").
	oldest := write(rotA, "z-vault.db.1", 1*time.Minute)
	mid := write(rotB, "a-other.db.1", 2*time.Minute)
	newer := write(rotA, "z-vault.db.2", 3*time.Minute)
	newest := write(rotB, "a-other.db.2", 4*time.Minute)

	t.Run("global-mtime-order-and-cap", func(t *testing.T) {
		got := newestRotatedSnapshots(
			filepath.Join(dirA, "vault.db"),
			filepath.Join(dirB, "vault.db"),
		)
		want := []string{newest, newer, mid}
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3 (cap): %v", len(got), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("got[%d] = %s, want %s", i, filepath.Base(got[i]), filepath.Base(want[i]))
			}
		}
		for _, g := range got {
			if g == oldest {
				t.Error("oldest copy must be dropped by the cap")
			}
		}
	})

	t.Run("dedup-shared-dir-and-empty-path", func(t *testing.T) {
		got := newestRotatedSnapshots(filepath.Join(dirA, "vault.db"), filepath.Join(dirA, "vault.db"), "")
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (dirA scanned once): %v", len(got), got)
		}
	})
}

// TestRestoreWithFallbackRotatedTier verifies the new tier end-to-end: when
// the primary snapshot is corrupt and no USB backup exists, the newest valid
// rotated copy restores and is reported as source "rotated" (#182).
func TestRestoreWithFallbackRotatedTier(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "vault.db")

	// Build a valid snapshot into the rotated dir by saving from a real DB.
	seed, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.CreateStorageDestination(db.StorageDestination{Name: "marker", Type: "local", Config: "{}"}); err != nil {
		t.Fatal(err)
	}
	rotated := filepath.Join(dir, "rotated")
	if err := os.MkdirAll(rotated, 0o755); err != nil {
		t.Fatal(err)
	}
	rotCopy := filepath.Join(rotated, "vault.db.2026-07-04T00-00-00.000000000")
	if err := db.NewSnapshotManager(seed, rotCopy, rotCopy).SaveSnapshot(); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()

	// Corrupt primary: exists but is not a database.
	if err := os.WriteFile(primary, []byte("not a sqlite file"), 0o644); err != nil {
		t.Fatal(err)
	}

	working, err := db.Open(filepath.Join(t.TempDir(), "working.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer working.Close()
	sm := db.NewSnapshotManager(working, primary, primary)

	info := restoreWithFallback(sm, primary, primary, filepath.Join(dir, "missing-usb.db"))
	if info.Source != "rotated" {
		t.Fatalf("restoration source = %q, want rotated (reason: %s)", info.Source, info.Reason)
	}
	// The marker row from the seed DB must have arrived in the working DB.
	dests, err := working.ListStorageDestinations()
	if err != nil || len(dests) != 1 || dests[0].Name != "marker" {
		t.Fatalf("rotated restore did not carry data: dests=%v err=%v", dests, err)
	}
}

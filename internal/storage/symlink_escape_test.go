package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalAdapterSymlinkEscape verifies adapter-level containment: a
// symlink planted under the destination root must not let List/Read/Delete
// reach outside the resolved base, while symlinks that stay inside the base
// (and bases that are themselves symlinks, e.g. /mnt/user fuse paths) keep
// working.
func TestLocalAdapterSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s3cret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	base := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "sneaky")); err != nil {
		t.Fatalf("create escape symlink: %v", err)
	}
	insideDir := filepath.Join(base, "real")
	if err := os.MkdirAll(insideDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(insideDir, "ok.txt"), []byte("fine"), 0o644); err != nil {
		t.Fatalf("write inside file: %v", err)
	}
	if err := os.Symlink(insideDir, filepath.Join(base, "alias")); err != nil {
		t.Fatalf("create internal symlink: %v", err)
	}

	a := NewLocalAdapter(base)

	// Escaping operations must be refused.
	if _, err := a.List("sneaky"); err == nil || !strings.Contains(err.Error(), "outside the destination root") {
		t.Errorf("List through escape symlink: err = %v, want containment error", err)
	}
	if _, err := a.Read("sneaky/secret.txt"); err == nil || !strings.Contains(err.Error(), "outside the destination root") {
		t.Errorf("Read through escape symlink: err = %v, want containment error", err)
	}
	if err := a.Delete("sneaky/secret.txt"); err == nil || !strings.Contains(err.Error(), "outside the destination root") {
		t.Errorf("Delete through escape symlink: err = %v, want containment error", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); err != nil {
		t.Fatalf("outside file should be untouched: %v", err)
	}

	// Symlinks that stay inside the base remain usable.
	if rc, err := a.Read("alias/ok.txt"); err != nil {
		t.Errorf("Read through internal symlink should work: %v", err)
	} else {
		_ = rc.Close()
	}

	// A base that is itself a symlink must keep working (resolved-to-resolved
	// comparison).
	linkBase := filepath.Join(t.TempDir(), "dest-link")
	if err := os.Symlink(base, linkBase); err != nil {
		t.Fatalf("create base symlink: %v", err)
	}
	b := NewLocalAdapter(linkBase)
	if rc, err := b.Read("real/ok.txt"); err != nil {
		t.Errorf("Read under symlinked base should work: %v", err)
	} else {
		_ = rc.Close()
	}
}

// TestPathWithinBase covers the SFTP containment comparison, including a
// base of "/" (previously rejected every child) and trailing separators.
func TestPathWithinBase(t *testing.T) {
	cases := []struct {
		resolved, base string
		want           bool
	}{
		{"/srv/backups/file", "/srv/backups", true},
		{"/srv/backups", "/srv/backups", true},
		{"/srv/backups2/file", "/srv/backups", false},
		{"/etc/passwd", "/srv/backups", false},
		{"/file", "/", true},
		{"/", "/", true},
		{"/srv/backups/file", "/srv/backups/", true},
	}
	for _, tc := range cases {
		if got := pathWithinBase(tc.resolved, tc.base); got != tc.want {
			t.Errorf("pathWithinBase(%q, %q) = %v, want %v", tc.resolved, tc.base, got, tc.want)
		}
	}
}

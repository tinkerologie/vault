package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeRestorePath(t *testing.T) {
	t.Parallel()

	tmpExpected, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatalf("EvalSymlinks(/tmp) error = %v", err)
	}

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "tmp allowed", input: "/tmp/vault-restore", want: filepath.Join(tmpExpected, "vault-restore")},
		{name: "mnt allowed", input: "/mnt/cache/vault", want: "/mnt/cache/vault"},
		{name: "dev rejected", input: "/dev/null", wantErr: true},
		{name: "relative rejected", input: "tmp/vault", wantErr: true},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeRestorePath(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("normalizeRestorePath() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("normalizeRestorePath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeRestorePathRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	allowedDir, err := os.MkdirTemp("/tmp", "vault-restore-")
	if err != nil {
		t.Fatalf("MkdirTemp(/tmp) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(allowedDir)
	})

	symlinkPath := filepath.Join(allowedDir, "devlink")
	if err := os.Symlink("/dev", symlinkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if _, err := normalizeRestorePath(filepath.Join(symlinkPath, "null")); err == nil {
		t.Fatal("normalizeRestorePath() should reject symlink traversal outside allowed roots")
	}
}

func TestNormalizeRestoreComponent(t *testing.T) {
	t.Parallel()

	if _, err := normalizeRestoreComponent("../../vault"); err == nil {
		t.Fatal("normalizeRestoreComponent() should reject path traversal")
	}

	got, err := normalizeRestoreComponent("vault.xml")
	if err != nil {
		t.Fatalf("normalizeRestoreComponent() error = %v", err)
	}
	if got != "vault.xml" {
		t.Fatalf("normalizeRestoreComponent() = %q, want %q", got, "vault.xml")
	}
}

func TestJoinArchiveTargetRejectsTraversal(t *testing.T) {
	t.Parallel()

	if _, err := joinArchiveTarget("/tmp/vault", "../evil"); err == nil {
		t.Fatal("joinArchiveTarget() should reject traversal entries")
	}
}

func TestResolveWithinBase(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	// Normal subdirectory should succeed.
	sub := filepath.Join(base, "subdir", "file.txt")
	if err := resolveWithinBase(base, sub); err != nil {
		t.Fatalf("resolveWithinBase() should accept path within base: %v", err)
	}

	// Path outside base should fail.
	outside := filepath.Join(base, "..", "evil.txt")
	if err := resolveWithinBase(base, outside); err == nil {
		t.Fatal("resolveWithinBase() should reject path outside base")
	}
}

func TestResolveWithinBaseRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	// Create a symlink inside base that points outside.
	if err := os.MkdirAll(filepath.Join(base, "subdir"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp", filepath.Join(base, "subdir", "escape")); err != nil {
		t.Fatal(err)
	}

	// A path through the escape symlink should be rejected.
	target := filepath.Join(base, "subdir", "escape", "evil.txt")
	if err := resolveWithinBase(base, target); err == nil {
		t.Fatal("resolveWithinBase() should reject path that escapes via symlink")
	}
}

func TestResolveSymlinkTargetRejectsAbsolute(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	symlinkPath := filepath.Join(base, "link")
	if err := resolveSymlinkTarget(base, symlinkPath, "/etc/passwd"); err == nil {
		t.Fatal("resolveSymlinkTarget() should reject absolute link targets")
	}
}

func TestResolveSymlinkTargetRejectsChainedEscape(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	// Create: subdir/parent -> .. (a symlink pointing to parent of subdir)
	if err := os.MkdirAll(filepath.Join(base, "subdir"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(base, "subdir", "parent")); err != nil {
		t.Fatal(err)
	}

	// Now a second symlink: escape -> subdir/parent/../..
	// Syntactically subdir/parent/.. = subdir/ but via symlink it resolves to
	// base/.. which is outside. The resolved target would be base/../.. = two
	// levels above base.
	symlinkPath := filepath.Join(base, "escape")
	linkTarget := "subdir/parent/../.."
	if err := resolveSymlinkTarget(base, symlinkPath, linkTarget); err == nil {
		t.Fatal("resolveSymlinkTarget() should reject chained symlink escape")
	}
}

func TestResolveSymlinkTargetAcceptsValid(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	// Create a real subdirectory.
	if err := os.MkdirAll(filepath.Join(base, "data"), 0750); err != nil {
		t.Fatal(err)
	}

	symlinkPath := filepath.Join(base, "link")
	if err := resolveSymlinkTarget(base, symlinkPath, "data"); err != nil {
		t.Fatalf("resolveSymlinkTarget() should accept valid link: %v", err)
	}
}

func TestVolumeRestoreTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		restoreDest string
		mountType   string
		volumeName  string
		source      string
		want        string
		wantErr     bool
	}{
		{
			name:        "empty restore dest returns original source",
			restoreDest: "",
			mountType:   "bind",
			source:      "/mnt/user/appdata/nginx",
			want:        "/mnt/user/appdata/nginx",
		},
		{
			name:        "custom dest with bind mount uses base of source",
			restoreDest: "/tmp/restore",
			mountType:   "bind",
			source:      "/mnt/user/appdata/nginx",
			want:        "/tmp/restore/nginx",
		},
		{
			name:        "custom dest with named volume uses volume name",
			restoreDest: "/tmp/restore",
			mountType:   "volume",
			volumeName:  "myvol",
			source:      "/var/lib/docker/volumes/myvol/_data",
			want:        "/tmp/restore/myvol",
		},
		{
			name:        "custom dest with named volume but empty name falls back to base source",
			restoreDest: "/tmp/restore",
			mountType:   "volume",
			volumeName:  "",
			source:      "/var/lib/docker/volumes/abc123/_data",
			want:        "/tmp/restore/_data",
		},
		{
			name:        "traversal attempt in volume name rejected",
			restoreDest: "/tmp/restore",
			mountType:   "volume",
			volumeName:  "../../etc",
			source:      "/var/lib/docker/volumes/evil/_data",
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := volumeRestoreTarget(tc.restoreDest, tc.mountType, tc.volumeName, tc.source)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("volumeRestoreTarget() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("volumeRestoreTarget() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("volumeRestoreTarget() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeVMRestorePlanRejectsUnsafeTarget(t *testing.T) {
	t.Parallel()

	plan := &vmRestorePlan{
		Disks: []vmRestoreDisk{{BackupFile: "vdisk0.qcow2", TargetPath: "/dev/null"}},
	}

	if err := normalizeVMRestorePlan(plan); err == nil {
		t.Fatal("normalizeVMRestorePlan() should reject unsafe targets")
	}
}

// TestCheckVolumeTargetCollisions guards custom-destination restores against
// silently merging two distinct volumes. volumeRestoreTarget keys on the base
// of the bind source, so sources that differ only above their last component
// collide on one directory — the restore would interleave both trees and point
// both binds at it, reporting success.
func TestCheckVolumeTargetCollisions(t *testing.T) {
	colliding := []mountInfo{
		{Type: "bind", Source: "/mnt/user/app/config", Destination: "/config"},
		{Type: "bind", Source: "/mnt/cache/other/config", Destination: "/other"},
	}

	// No custom destination: every volume returns to its own source, so the
	// shared basename is harmless and must not be rejected.
	if err := checkVolumeTargetCollisions("", colliding); err != nil {
		t.Fatalf("collision reported without a custom destination: %v", err)
	}

	err := checkVolumeTargetCollisions("/mnt/user/restore", colliding)
	if err == nil {
		t.Fatal("two mounts resolving to the same target were accepted — " +
			"the restore would merge them and report success")
	}
	for _, want := range []string{"/mnt/user/app/config", "/mnt/cache/other/config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the colliding source %q, got: %v", want, err)
		}
	}

	distinct := []mountInfo{
		{Type: "bind", Source: "/mnt/user/app/config", Destination: "/config"},
		{Type: "bind", Source: "/mnt/user/app/data", Destination: "/data"},
	}
	if err := checkVolumeTargetCollisions("/mnt/user/restore", distinct); err != nil {
		t.Fatalf("distinct basenames rejected: %v", err)
	}
}

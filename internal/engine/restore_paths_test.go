package engine

import (
	"os"
	"path/filepath"
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

func TestNormalizeVMRestorePlanRejectsUnsafeTarget(t *testing.T) {
	t.Parallel()

	plan := &vmRestorePlan{
		Disks: []vmRestoreDisk{{BackupFile: "vdisk0.qcow2", TargetPath: "/dev/null"}},
	}

	if err := normalizeVMRestorePlan(plan); err == nil {
		t.Fatal("normalizeVMRestorePlan() should reject unsafe targets")
	}
}

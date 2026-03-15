package engine

import "testing"

func TestNormalizeRestorePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "tmp allowed", input: "/tmp/vault-restore", want: "/tmp/vault-restore"},
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

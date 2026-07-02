package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortSHA truncates a sha256:… digest to its first 12 hex chars, for log
// lines. Anything shorter than 12 chars passes through unchanged so callers
// don't crash on test fixtures or partial digests.
func TestShortSHA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"full digest", "sha256:1234567890abcdef1234567890abcdef", "sha256:1234567890ab"},
		{"already short", "sha256:abc", "sha256:abc"},
		{"missing prefix", "1234567890abcdef1234567890abcdef", "sha256:1234567890ab"},
		{"empty", "", "sha256:"},
		{"prefix only", "sha256:", "sha256:"},
		{"exactly 12 chars", "sha256:123456789012", "sha256:123456789012"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shortSHA(tc.in)
			if got != tc.want {
				t.Fatalf("shortSHA(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// vmRestoreVerifyTimeout returns the configured timeout when positive, else
// the package default. This guard exists so callers never pass a 0-second
// deadline to net.DialTimeout (which would short-circuit immediately).
func TestVMRestoreVerifyTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   vmRestoreVerifyConfig
		want int
	}{
		{"zero falls back to default", vmRestoreVerifyConfig{}, defaultVMRestoreVerifyTimeoutSecs},
		{"negative falls back to default", vmRestoreVerifyConfig{TimeoutSeconds: -1}, defaultVMRestoreVerifyTimeoutSecs},
		{"positive used as-is", vmRestoreVerifyConfig{TimeoutSeconds: 300}, 300},
		{"one second is honoured", vmRestoreVerifyConfig{TimeoutSeconds: 1}, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := vmRestoreVerifyTimeout(tc.in)
			if got != tc.want {
				t.Fatalf("vmRestoreVerifyTimeout(%+v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// normalizeVMRestorePlan iterates over the plan's disks, normalising each
// BackupFile and TargetPath, plus the optional NVRAM fields. The happy
// path is otherwise only exercised indirectly via buildVMRestorePlan —
// here we drive it directly with a fully-populated plan to cover the
// NVRAM-present branches alongside the disk loop.
func TestNormalizeVMRestorePlanSuccessPath(t *testing.T) {
	t.Parallel()

	plan := &vmRestorePlan{
		Disks: []vmRestoreDisk{
			{BackupFile: "vdisk0.qcow2", TargetPath: "/tmp/vault-restore-test/vdisk0.qcow2"},
		},
		NVRAMBackupFile: "vm_VARS.fd",
		NVRAMTargetPath: "/tmp/vault-restore-test/vm_VARS.fd",
	}

	if err := normalizeVMRestorePlan(plan); err != nil {
		t.Fatalf("normalizeVMRestorePlan: %v", err)
	}
	if plan.NVRAMBackupFile != "vm_VARS.fd" {
		t.Fatalf("NVRAMBackupFile = %q, want vm_VARS.fd", plan.NVRAMBackupFile)
	}
	if plan.Disks[0].BackupFile != "vdisk0.qcow2" {
		t.Fatalf("Disks[0].BackupFile = %q, want vdisk0.qcow2", plan.Disks[0].BackupFile)
	}
}

// writeVMBackupMetadata returns an error when the supplied settings make
// vmRestoreVerifyConfigFromSettings reject the input — e.g. tcp mode
// without a port. This guards the metadata file from ever being written
// with a config that subsequent restore checks could not validate.
func TestWriteVMBackupMetadataRejectsInvalidSettings(t *testing.T) {
	t.Parallel()

	_, err := writeVMBackupMetadata(t.TempDir(), "running", map[string]any{
		"restore_verify_mode": "tcp", // tcp without a port is invalid
	})
	if err == nil {
		t.Fatal("expected error for tcp mode without port")
	}
}

// settingInt and settingString are the type-coercion helpers used to read
// per-job settings from the JSON-decoded map[string]any. settingInt's
// branches cover every legal JSON-encoded form (nil, int, int32, int64,
// float64, numeric string) plus the rejection branches for fractional
// floats and unsupported types.
func TestSettingIntAllForms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      any
		want    int
		wantErr bool
	}{
		{"nil", nil, 0, false},
		{"int", int(7), 7, false},
		{"int32", int32(8), 8, false},
		{"int64", int64(9), 9, false},
		{"float64 whole", float64(10), 10, false},
		{"float64 fractional rejected", float64(10.5), 0, true},
		{"string numeric", "11", 11, false},
		{"string empty returns zero", "   ", 0, false},
		{"string non-numeric rejected", "abc", 0, true},
		{"unsupported type rejected", true, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := settingInt(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("settingInt(%v) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("settingInt(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestSettingStringFallbacks(t *testing.T) {
	t.Parallel()

	if got := settingString(nil); got != "" {
		t.Fatalf("settingString(nil) = %q, want empty", got)
	}
	if got := settingString("hello"); got != "hello" {
		t.Fatalf("settingString(string) = %q", got)
	}
	// Non-string falls through to fmt.Sprint formatting.
	if got := settingString(42); got != "42" {
		t.Fatalf("settingString(int) = %q, want 42", got)
	}
}

// sameDomainDisks must return true only when both slices contain identical
// domainDisk entries in the same order. Any diff in length, target, path,
// or index breaks equality.
func TestSameDomainDisks(t *testing.T) {
	t.Parallel()

	a := []domainDisk{
		{Index: 0, Path: "/mnt/cache/domains/vm/disk.qcow2", Target: "vda"},
		{Index: 1, Path: "/mnt/cache/domains/vm/data.img", Target: "vdb"},
	}
	identical := []domainDisk{
		{Index: 0, Path: "/mnt/cache/domains/vm/disk.qcow2", Target: "vda"},
		{Index: 1, Path: "/mnt/cache/domains/vm/data.img", Target: "vdb"},
	}
	differentLen := []domainDisk{
		{Index: 0, Path: "/mnt/cache/domains/vm/disk.qcow2", Target: "vda"},
	}
	differentTarget := []domainDisk{
		{Index: 0, Path: "/mnt/cache/domains/vm/disk.qcow2", Target: "sda"},
		{Index: 1, Path: "/mnt/cache/domains/vm/data.img", Target: "vdb"},
	}
	differentPath := []domainDisk{
		{Index: 0, Path: "/mnt/cache/domains/vm/disk-renamed.qcow2", Target: "vda"},
		{Index: 1, Path: "/mnt/cache/domains/vm/data.img", Target: "vdb"},
	}

	if !sameDomainDisks(a, identical) {
		t.Fatal("identical disk slices must compare equal")
	}
	if sameDomainDisks(a, differentLen) {
		t.Fatal("slices of differing length must not compare equal")
	}
	if sameDomainDisks(a, differentTarget) {
		t.Fatal("differing target devices must break equality")
	}
	if sameDomainDisks(a, differentPath) {
		t.Fatal("differing source paths must break equality")
	}
	if !sameDomainDisks(nil, []domainDisk{}) {
		t.Fatal("nil and empty-slice should be considered equal (both length zero)")
	}
}

// updateVMBackupMetadata reads vm_meta.json, applies a mutator, and writes
// it back. The function is small but error-prone — it must surface I/O
// errors from missing/unreadable files and JSON parse failures so callers
// don't silently lose checkpoint metadata after an incremental backup.
func TestUpdateVMBackupMetadataAppliesMutator(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Seed via the public writer so we exercise the same shape the production
	// code consumes (state field plus default restore_verify block).
	if _, err := writeVMBackupMetadata(dir, "running", nil); err != nil {
		t.Fatalf("writeVMBackupMetadata: %v", err)
	}

	err := updateVMBackupMetadata(dir, func(m *vmBackupMetadata) {
		m.BackupType = "incremental"
		m.Checkpoint = "vault-bk-1"
		m.ParentCheckpoint = "vault-bk-0"
		m.Disks = []vmDiskMeta{{Target: "vda", BackupFile: "vdisk0.qcow2", Format: "qcow2"}}
	})
	if err != nil {
		t.Fatalf("updateVMBackupMetadata: %v", err)
	}

	// Re-read raw JSON to confirm the mutator's changes round-tripped.
	data, err := os.ReadFile(filepath.Join(dir, vmMetadataFileName))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got vmBackupMetadata
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.BackupType != "incremental" {
		t.Fatalf("BackupType = %q, want incremental", got.BackupType)
	}
	if got.Checkpoint != "vault-bk-1" {
		t.Fatalf("Checkpoint = %q, want vault-bk-1", got.Checkpoint)
	}
	if got.ParentCheckpoint != "vault-bk-0" {
		t.Fatalf("ParentCheckpoint = %q, want vault-bk-0", got.ParentCheckpoint)
	}
	if len(got.Disks) != 1 || got.Disks[0].Target != "vda" {
		t.Fatalf("Disks = %+v, want [{vda vdisk0.qcow2 qcow2}]", got.Disks)
	}
	// State written by writeVMBackupMetadata must survive the update.
	if got.State != "running" {
		t.Fatalf("State = %q, want running", got.State)
	}
}

func TestUpdateVMBackupMetadataMissingFile(t *testing.T) {
	t.Parallel()

	err := updateVMBackupMetadata(t.TempDir(), func(m *vmBackupMetadata) {})
	if err == nil {
		t.Fatal("expected error when vm_meta.json is absent")
	}
	if !strings.Contains(err.Error(), "read vm metadata") {
		t.Fatalf("expected read-vm-metadata error wrap, got %v", err)
	}
}

func TestUpdateVMBackupMetadataInvalidJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, vmMetadataFileName), []byte("not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err := updateVMBackupMetadata(dir, func(m *vmBackupMetadata) {})
	if err == nil {
		t.Fatal("expected JSON parse error")
	}
	if !strings.Contains(err.Error(), "parse vm metadata") {
		t.Fatalf("expected parse-vm-metadata error wrap, got %v", err)
	}
}

// TestVMDiskMetaJSONContract pins the vm_meta.json disk-record key names. The
// runner's chain-restore code (internal/runner/vm_chain.go vmChainStepMeta)
// decodes these fields independently, so a renamed tag here would silently
// break incremental VM restores.
func TestVMDiskMetaJSONContract(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(vmDiskMeta{Target: "hdc", BackupFile: "vdisk0.img", Format: "qcow2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"target"`, `"backup_file"`, `"format"`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("vm_meta.json disk record lost key %s: %s", key, data)
		}
	}
}

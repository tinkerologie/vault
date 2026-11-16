package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/safepath"
)

func TestInspectApprovedVMBackupRequiresRecordedDiskAndFirmware(t *testing.T) {
	xml := []byte(`<domain><name>fixture</name><uuid>22222222-3333-4444-aaaa-555555555555</uuid>
	<os><nvram>/mnt/user/domains/fixture/vars.fd</nvram></os>
	<devices><disk type="file" device="disk"><source file="/mnt/user/domains/fixture/disk.img"/><target dev="vda"/></disk></devices>
	</domain>`)
	metadata := []byte(`{"backup_type":"full","state":"shutoff","disks":[{"target":"vda","backup_file":"vdisk0.img","format":"raw"}]}`)
	files := []string{"vdisk0.img", "vars.fd"}
	got, err := InspectVMBackup(xml, metadata, files)
	if err != nil || got.Name != "fixture" || got.BackupType != "full" || len(got.Disks) != 1 || got.Disks[0].BackupFile != "vdisk0.img" {
		t.Fatalf("evidence = %+v, err = %v", got, err)
	}

	if _, err := InspectVMBackup(xml, metadata, files[1:]); !errors.Is(err, ErrVMBackupArtifactMissing) {
		t.Fatalf("missing disk = %v", err)
	}
	if _, err := InspectVMBackup(xml, metadata, files[:1]); !errors.Is(err, ErrVMBackupArtifactMissing) {
		t.Fatalf("missing firmware = %v", err)
	}
	unsafe := []byte(strings.ReplaceAll(string(metadata), "vdisk0.img", "../vdisk0.img"))
	if _, err := InspectVMBackup(xml, unsafe, files); !errors.Is(err, ErrVMBackupEvidenceUnknown) {
		t.Fatalf("unsafe metadata = %v", err)
	}
}

func TestConfineVMReviewTargetRefusesPathsOutsideRestoreRoots(t *testing.T) {
	for _, p := range []string{
		"/root/disk.img",
		"/mnt/../root/disk.img",
		"/mnt/user/domains/../../../root/disk.img",
		"mnt/user/domains/disk.img",
		" /mnt/user/domains/disk.img",
		"",
	} {
		if _, _, err := confineVMReviewTarget(p, safepath.RestoreAllowedRoots()); !errors.Is(err, ErrVMRestoreTargetDisallowed) {
			t.Errorf("confineVMReviewTarget(%q) error = %v, want %v", p, err, ErrVMRestoreTargetDisallowed)
		}
	}
	got, root, err := confineVMReviewTarget("/mnt/user//domains/fixture/disk.img", safepath.RestoreAllowedRoots())
	if err != nil || got != "/mnt/user/domains/fixture/disk.img" || root != "/mnt" {
		t.Fatalf("confineVMReviewTarget = %q, %q, %v", got, root, err)
	}
}

func TestNearestVMReviewParentNeverLeavesRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	target := filepath.Join(root, "a", "b", "disk.img")
	// The root does not exist, but its parent does: the walk must stop at
	// the root instead of reporting the directory above it.
	if parent, _, err := nearestVMReviewParent(target, root); !errors.Is(err, ErrVMRestoreHostUnavailable) {
		t.Fatalf("nearestVMReviewParent = %q, %v; want %v", parent, err, ErrVMRestoreHostUnavailable)
	}
	if parent, _, err := nearestVMReviewParent(root, root); !errors.Is(err, ErrVMRestoreHostUnavailable) {
		t.Fatalf("nearestVMReviewParent(root) = %q, %v; want %v", parent, err, ErrVMRestoreHostUnavailable)
	}
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	parent, info, err := nearestVMReviewParent(target, root)
	if err != nil || parent != filepath.Join(root, "a") || !info.IsDir() {
		t.Fatalf("nearestVMReviewParent = %q, %v, %v", parent, info, err)
	}
}

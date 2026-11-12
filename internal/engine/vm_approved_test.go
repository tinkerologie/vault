package engine

import (
	"errors"
	"strings"
	"testing"
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

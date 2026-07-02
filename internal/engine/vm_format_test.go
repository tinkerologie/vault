package engine

import (
	"strings"
	"testing"
)

// Unraid names VM disk images vdisk1.img regardless of their real format, so
// the format must come from the domain XML <driver type=...>, not the file
// extension. Issue #160: a qcow2-formatted .img was backed up and restored as
// raw, producing an unbootable restore.
func TestParseDomainDisksCapturesDriverFormat(t *testing.T) {
	t.Parallel()

	xmlDesc := `<domain>
  <devices>
    <disk type="file" device="disk">
      <driver name="qemu" type="qcow2" cache="writeback"></driver>
      <source file="/mnt/disks/ssd/VM/win/vdisk1.img"></source>
      <target dev="hdc"></target>
    </disk>
    <disk type="file" device="disk">
      <driver name="qemu" type="raw"></driver>
      <source file="/mnt/disks/ssd/VM/centos/vdisk1.img"></source>
      <target dev="hdd"></target>
    </disk>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
      <target dev="vda"></target>
    </disk>
  </devices>
</domain>`

	disks, _, err := parseDomainDisksWithTargets(xmlDesc)
	if err != nil {
		t.Fatalf("parseDomainDisksWithTargets() error = %v", err)
	}
	if len(disks) != 3 {
		t.Fatalf("expected 3 disks, got %d", len(disks))
	}

	if disks[0].Format != "qcow2" {
		t.Fatalf("expected qcow2 format from driver type for .img disk, got %q", disks[0].Format)
	}
	if disks[1].Format != "raw" {
		t.Fatalf("expected raw format from driver type, got %q", disks[1].Format)
	}
	// No <driver> element: fall back to the extension heuristic.
	if disks[2].Format != "qcow2" {
		t.Fatalf("expected qcow2 format from extension fallback, got %q", disks[2].Format)
	}
}

func TestParseDomainDiskInventoryReportsSkippedDisks(t *testing.T) {
	t.Parallel()

	xmlDesc := `<domain>
  <devices>
    <disk type="block" device="disk">
      <driver name="qemu" type="raw"></driver>
      <source dev="/dev/disk/by-id/ata-Samsung"></source>
      <target dev="hdc"></target>
    </disk>
    <disk type="file" device="cdrom">
      <source file="/isos/installer.iso"></source>
      <target dev="hda"></target>
    </disk>
  </devices>
</domain>`

	inventory, err := parseDomainDiskInventory(xmlDesc)
	if err != nil {
		t.Fatalf("parseDomainDiskInventory() error = %v", err)
	}
	if len(inventory.Disks) != 0 {
		t.Fatalf("expected no eligible disks, got %d", len(inventory.Disks))
	}
	// The cdrom is not a disk device and must not be reported; the
	// block-backed disk must be surfaced so the backup can fail loudly
	// instead of silently producing an empty backup.
	if len(inventory.Skipped) != 1 {
		t.Fatalf("expected 1 skipped disk, got %d: %+v", len(inventory.Skipped), inventory.Skipped)
	}
	if inventory.Skipped[0].Target != "hdc" || inventory.Skipped[0].SourceType != "block" {
		t.Fatalf("unexpected skipped disk: %+v", inventory.Skipped[0])
	}
	if inventory.Skipped[0].SourcePath != "/dev/disk/by-id/ata-Samsung" {
		t.Fatalf("expected skipped disk to carry its block source path, got %q", inventory.Skipped[0].SourcePath)
	}
}

func TestFormatSkippedDomainDisks(t *testing.T) {
	t.Parallel()

	desc := formatSkippedDomainDisks([]skippedDomainDisk{
		{Target: "hdc", SourceType: "block", SourcePath: "/dev/disk/by-id/ata-Samsung"},
		{Target: "vdb", SourceType: "network"},
	})
	if !strings.Contains(desc, "hdc") || !strings.Contains(desc, "block") {
		t.Fatalf("expected skipped disk description to name target and source type: %s", desc)
	}
	if !strings.Contains(desc, "/dev/disk/by-id/ata-Samsung") {
		t.Fatalf("expected skipped disk description to include the block source path: %s", desc)
	}
}

func TestBuildBackupXMLUsesDomainDriverFormat(t *testing.T) {
	t.Parallel()

	xmlDesc, artifacts, err := buildBackupXMLWithParent("/tmp/vault-backup", []domainDisk{
		{Index: 0, Path: "/mnt/disks/ssd/VM/win/vdisk1.img", Target: "hdc", Format: "qcow2"},
	}, "", false)
	if err != nil {
		t.Fatalf("buildBackupXMLWithParent() error = %v", err)
	}

	if !strings.Contains(xmlDesc, `<driver type="qcow2"></driver>`) {
		t.Fatalf("expected backup XML to use the domain driver format qcow2: %s", xmlDesc)
	}
	if artifacts[0].BackupFile != "vdisk0.img" {
		t.Fatalf("expected backup file to keep the source extension, got %q", artifacts[0].BackupFile)
	}
	if artifacts[0].Format != "qcow2" {
		t.Fatalf("expected artifact format qcow2, got %q", artifacts[0].Format)
	}
}

func TestAllDisksQcow2UsesFormat(t *testing.T) {
	t.Parallel()

	qcow2Img := []domainDisk{
		{Index: 0, Path: "/mnt/user/domains/win/vdisk1.img", Target: "hdc", Format: "qcow2"},
	}
	if !allDisksQcow2(qcow2Img) {
		t.Fatal("expected qcow2-format .img disks to be incremental-eligible")
	}

	mixed := []domainDisk{
		{Index: 0, Path: "/mnt/user/domains/win/vdisk1.img", Target: "hdc", Format: "qcow2"},
		{Index: 1, Path: "/mnt/user/domains/win/vdisk2.img", Target: "hdd", Format: "raw"},
	}
	if allDisksQcow2(mixed) {
		t.Fatal("expected mixed-format disks to be rejected")
	}

	if allDisksQcow2(nil) {
		t.Fatal("expected empty disk list to be rejected")
	}

	// Legacy callers may construct disks without Format — fall back to the
	// extension heuristic.
	legacy := []domainDisk{
		{Index: 0, Path: "/mnt/cache/domains/haos/haos.qcow2", Target: "vda"},
	}
	if !allDisksQcow2(legacy) {
		t.Fatal("expected extension fallback for disks without Format")
	}
}

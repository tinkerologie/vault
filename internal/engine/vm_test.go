package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVMBackupResultTypes(t *testing.T) {
	result := &BackupResult{
		ItemName: "test-vm",
		Success:  true,
		Files: []BackupFile{
			{Name: "domain.xml", Size: 4096},
			{Name: "vdisk0.qcow2", Size: 10737418240},
		},
	}
	if !result.Success {
		t.Error("expected success")
	}
	if len(result.Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(result.Files))
	}
}

func TestCopyFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source.bin")
	dst := filepath.Join(t.TempDir(), "dest.bin")

	data := []byte("test vm disk data")
	os.WriteFile(src, data, 0644)

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile() error = %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

func TestCopyFileWithProgress(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source.bin")
	dst := filepath.Join(t.TempDir(), "dest.bin")

	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte(i % 256)
	}
	os.WriteFile(src, data, 0644)

	var progressCalled bool
	err := copyFileWithProgress(src, dst, func(bytesCopied int64) {
		progressCalled = true
	})
	if err != nil {
		t.Fatalf("copyFileWithProgress() error = %v", err)
	}
	if !progressCalled {
		t.Error("expected progress callback to be called")
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if len(got) != len(data) {
		t.Errorf("got %d bytes, want %d", len(got), len(data))
	}
}

func TestCopyFileSourceNotFound(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dest.bin")
	err := copyFile("/nonexistent/file.bin", dst)
	if err == nil {
		t.Fatal("expected error for missing source file")
	}
}

func TestNewVMHandlerPlatform(t *testing.T) {
	// On non-Linux platforms, NewVMHandler should return an error.
	// On Linux without libvirt, it will also return an error.
	// Either way, we just verify the function exists and returns.
	_, err := NewVMHandler()
	if err == nil {
		t.Skip("libvirt available, skipping platform check")
	}
	t.Logf("NewVMHandler() returned expected error: %v", err)
}

func TestParseDomainDisksWithTargets(t *testing.T) {
	t.Parallel()

	xmlDesc := `<domain>
  <devices>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
      <target dev="vda"></target>
    </disk>
    <disk type="file" device="disk">
      <source file="/mnt/user/domains/data.qcow2"></source>
      <target dev="sdb"></target>
    </disk>
    <disk type="file" device="cdrom">
      <source file="/isos/installer.iso"></source>
      <target dev="hdc"></target>
    </disk>
  </devices>
  <os>
    <nvram>/etc/libvirt/qemu/nvram/Home_Assistant_VARS.fd</nvram>
  </os>
</domain>`

	disks, nvramPath, err := parseDomainDisksWithTargets(xmlDesc)
	if err != nil {
		t.Fatalf("parseDomainDisksWithTargets() error = %v", err)
	}

	if len(disks) != 2 {
		t.Fatalf("expected 2 disks, got %d", len(disks))
	}

	if disks[0].Index != 0 || disks[0].Target != "vda" || disks[0].Path != "/mnt/cache/domains/haos/haos.qcow2" {
		t.Fatalf("unexpected first disk: %+v", disks[0])
	}

	if disks[1].Index != 1 || disks[1].Target != "sdb" || disks[1].Path != "/mnt/user/domains/data.qcow2" {
		t.Fatalf("unexpected second disk: %+v", disks[1])
	}

	if nvramPath != "/etc/libvirt/qemu/nvram/Home_Assistant_VARS.fd" {
		t.Fatalf("unexpected nvram path: %q", nvramPath)
	}
}

func TestBuildSnapshotXMLUsesDiskTargets(t *testing.T) {
	t.Parallel()

	snapshotXML, err := buildSnapshotXML("Home Assistant", []domainDisk{{
		Path:   "/mnt/cache/domains/haos/haos.qcow2",
		Target: "vda",
	}})
	if err != nil {
		t.Fatalf("buildSnapshotXML() error = %v", err)
	}

	if !strings.Contains(snapshotXML, `<disk name="vda" snapshot="external">`) {
		t.Fatalf("snapshot XML did not use target device name: %s", snapshotXML)
	}

	if strings.Contains(snapshotXML, `name="/mnt/cache/domains/haos/haos.qcow2"`) {
		t.Fatalf("snapshot XML used disk path as name: %s", snapshotXML)
	}

	if !strings.Contains(snapshotXML, `file="/mnt/cache/domains/haos/haos.qcow2.snap"`) {
		t.Fatalf("snapshot XML missing external overlay path: %s", snapshotXML)
	}
}

func TestBuildSnapshotXMLRequiresTarget(t *testing.T) {
	t.Parallel()

	_, err := buildSnapshotXML("vm", []domainDisk{{Path: "/mnt/cache/domains/haos/haos.qcow2"}})
	if err == nil {
		t.Fatal("expected error when disk target is missing")
	}
}

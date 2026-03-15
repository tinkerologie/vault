package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
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

func TestBuildVMRestorePlanRejectsUnsafeRestoreDestination(t *testing.T) {
	t.Parallel()

	xmlDesc := `<domain>
  <devices>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
      <target dev="vda"></target>
    </disk>
  </devices>
</domain>`

	if _, err := buildVMRestorePlan([]byte(xmlDesc), "/dev/shm/haos"); err == nil {
		t.Fatal("buildVMRestorePlan() should reject restore destinations outside allowed roots")
	}
}

func TestBuildVMRestorePlanRejectsSymlinkedRestoreDestinationEscape(t *testing.T) {
	t.Parallel()

	xmlDesc := `<domain>
  <devices>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
      <target dev="vda"></target>
    </disk>
  </devices>
</domain>`

	allowedDir, err := os.MkdirTemp("/tmp", "vault-vm-restore-")
	if err != nil {
		t.Fatalf("MkdirTemp(/tmp) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(allowedDir)
	})

	symlinkPath := filepath.Join(allowedDir, "escape")
	if err := os.Symlink("/dev", symlinkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if _, err := buildVMRestorePlan([]byte(xmlDesc), symlinkPath); err == nil {
		t.Fatal("buildVMRestorePlan() should reject symlinked restore destinations that escape allowed roots")
	}
}

func TestVMBackupMetadataStartAfterRestore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	metadataPath, err := writeVMBackupMetadata(dir, "running", nil)
	if err != nil {
		t.Fatalf("writeVMBackupMetadata() error = %v", err)
	}
	if filepath.Base(metadataPath) != vmMetadataFileName {
		t.Fatalf("metadata file = %q, want %q", filepath.Base(metadataPath), vmMetadataFileName)
	}

	metadata, err := readVMRestoreMetadata(dir)
	if err != nil {
		t.Fatalf("readVMRestoreMetadata() error = %v", err)
	}
	if metadata.State != "running" {
		t.Fatalf("metadata.State = %q, want %q", metadata.State, "running")
	}
	if !metadata.startAfterRestore() {
		t.Fatal("metadata.startAfterRestore() should be true for running VMs")
	}
	if metadata.RestoreVerify.Mode != vmRestoreVerifyModeRunning {
		t.Fatalf("metadata.RestoreVerify.Mode = %q, want %q", metadata.RestoreVerify.Mode, vmRestoreVerifyModeRunning)
	}

	stoppedPath, err := writeVMBackupMetadata(dir, "shut off", nil)
	if err != nil {
		t.Fatalf("writeVMBackupMetadata(stopped) error = %v", err)
	}
	if stoppedPath != metadataPath {
		t.Fatalf("metadata path changed between writes: %q vs %q", stoppedPath, metadataPath)
	}

	metadata, err = readVMRestoreMetadata(dir)
	if err != nil {
		t.Fatalf("readVMRestoreMetadata(stopped) error = %v", err)
	}
	if metadata.startAfterRestore() {
		t.Fatal("metadata.startAfterRestore() should be false for non-running VMs")
	}
}

func TestVMRestoreVerifyConfigFromSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings map[string]any
		want     vmRestoreVerifyConfig
		wantErr  bool
	}{
		{
			name:     "defaults to running",
			settings: map[string]any{},
			want: vmRestoreVerifyConfig{
				Mode:           vmRestoreVerifyModeRunning,
				TimeoutSeconds: defaultVMRestoreVerifyTimeoutSecs,
			},
		},
		{
			name: "guest agent with custom timeout",
			settings: map[string]any{
				"restore_verify_mode":            "guest_agent",
				"restore_verify_timeout_seconds": 180,
			},
			want: vmRestoreVerifyConfig{
				Mode:           vmRestoreVerifyModeGuestAgent,
				TimeoutSeconds: 180,
			},
		},
		{
			name: "tcp with auto detected host",
			settings: map[string]any{
				"restore_verify_mode":     "tcp",
				"restore_verify_tcp_port": float64(8123),
			},
			want: vmRestoreVerifyConfig{
				Mode:           vmRestoreVerifyModeTCP,
				TimeoutSeconds: defaultVMRestoreVerifyTimeoutSecs,
				TCPPort:        8123,
			},
		},
		{
			name: "tcp with explicit host",
			settings: map[string]any{
				"restore_verify_mode":            "tcp",
				"restore_verify_timeout_seconds": "90",
				"restore_verify_tcp_host":        "ha.local",
				"restore_verify_tcp_port":        "8123",
			},
			want: vmRestoreVerifyConfig{
				Mode:           vmRestoreVerifyModeTCP,
				TimeoutSeconds: 90,
				TCPHost:        "ha.local",
				TCPPort:        8123,
			},
		},
		{
			name: "rejects tcp without port",
			settings: map[string]any{
				"restore_verify_mode": "tcp",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := vmRestoreVerifyConfigFromSettings(tt.settings)
			if (err != nil) != tt.wantErr {
				t.Fatalf("vmRestoreVerifyConfigFromSettings() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Fatalf("vmRestoreVerifyConfigFromSettings() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestPickVMReadyAddressFromInterfaces(t *testing.T) {
	t.Parallel()

	ifaces := []libvirt.DomainInterface{
		{
			Name: "eth0",
			Addrs: []libvirt.DomainIPAddr{
				{Type: int32(libvirt.IPAddrTypeIpv6), Addr: "fe80::1", Prefix: 64},
				{Type: int32(libvirt.IPAddrTypeIpv4), Addr: "192.168.20.50", Prefix: 24},
			},
		},
	}

	got := pickVMReadyAddressFromInterfaces(ifaces)
	if got != "192.168.20.50" {
		t.Fatalf("pickVMReadyAddressFromInterfaces() = %q, want %q", got, "192.168.20.50")
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

func TestSelectBackupDiskXMLUsesLiveXMLForRunningVMs(t *testing.T) {
	t.Parallel()

	liveXML := `<domain>
  <devices>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2.snap"></source>
      <target dev="hdc"></target>
    </disk>
  </devices>
</domain>`
	inactiveXML := `<domain>
  <devices>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
      <target dev="hdc"></target>
    </disk>
  </devices>
</domain>`

	selected := selectBackupDiskXML(libvirt.DomainRunning, liveXML, inactiveXML)
	disks, _, err := parseDomainDisksWithTargets(selected)
	if err != nil {
		t.Fatalf("parseDomainDisksWithTargets() error = %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(disks))
	}
	if disks[0].Path != "/mnt/cache/domains/haos/haos.qcow2.snap" {
		t.Fatalf("expected running VM backup to use live XML disk path, got %q", disks[0].Path)
	}
}

func TestSelectBackupDiskXMLUsesInactiveXMLForStoppedVMs(t *testing.T) {
	t.Parallel()

	liveXML := `<domain>
  <devices>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2.snap"></source>
      <target dev="hdc"></target>
    </disk>
  </devices>
</domain>`
	inactiveXML := `<domain>
  <devices>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
      <target dev="hdc"></target>
    </disk>
  </devices>
</domain>`

	selected := selectBackupDiskXML(libvirt.DomainShutoff, liveXML, inactiveXML)
	disks, _, err := parseDomainDisksWithTargets(selected)
	if err != nil {
		t.Fatalf("parseDomainDisksWithTargets() error = %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(disks))
	}
	if disks[0].Path != "/mnt/cache/domains/haos/haos.qcow2" {
		t.Fatalf("expected stopped VM backup to use inactive XML disk path, got %q", disks[0].Path)
	}
}

func TestBuildVMRestorePlanKeepsOriginalPaths(t *testing.T) {
	t.Parallel()

	xmlDesc := `<domain>
  <devices>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
      <target dev="vda"></target>
    </disk>
  </devices>
  <os>
    <nvram>/etc/libvirt/qemu/nvram/Home_Assistant_VARS.fd</nvram>
  </os>
</domain>`

	plan, err := buildVMRestorePlan([]byte(xmlDesc), "")
	if err != nil {
		t.Fatalf("buildVMRestorePlan() error = %v", err)
	}

	if len(plan.Disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(plan.Disks))
	}

	if plan.Disks[0].BackupFile != "vdisk0.qcow2" {
		t.Fatalf("unexpected backup file: %q", plan.Disks[0].BackupFile)
	}

	if plan.Disks[0].TargetPath != "/mnt/cache/domains/haos/haos.qcow2" {
		t.Fatalf("unexpected target path: %q", plan.Disks[0].TargetPath)
	}

	if plan.NVRAMBackupFile != "Home_Assistant_VARS.fd" {
		t.Fatalf("unexpected NVRAM backup file: %q", plan.NVRAMBackupFile)
	}

	if plan.NVRAMTargetPath != "/etc/libvirt/qemu/nvram/Home_Assistant_VARS.fd" {
		t.Fatalf("unexpected NVRAM target path: %q", plan.NVRAMTargetPath)
	}

	if plan.DomainXML != xmlDesc {
		t.Fatalf("expected original XML to be preserved when no restore destination is provided")
	}
}

func TestBuildVMRestorePlanRewritesRestoreDestination(t *testing.T) {
	t.Parallel()

	xmlDesc := `<domain>
  <devices>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
      <target dev="vda"></target>
    </disk>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/data.img"></source>
      <target dev="vdb"></target>
    </disk>
  </devices>
  <os>
    <nvram>/etc/libvirt/qemu/nvram/Home_Assistant_VARS.fd</nvram>
  </os>
</domain>`

	restoreDest := "/tmp/vault-restore/haos"
	normalizedRestoreDest, err := normalizeRestorePath(restoreDest)
	if err != nil {
		t.Fatalf("normalizeRestorePath() error = %v", err)
	}

	plan, err := buildVMRestorePlan([]byte(xmlDesc), restoreDest)
	if err != nil {
		t.Fatalf("buildVMRestorePlan() error = %v", err)
	}

	if len(plan.Disks) != 2 {
		t.Fatalf("expected 2 disks, got %d", len(plan.Disks))
	}

	if plan.Disks[0].TargetPath != filepath.Join(normalizedRestoreDest, "haos.qcow2") {
		t.Fatalf("unexpected first target path: %q", plan.Disks[0].TargetPath)
	}

	if plan.Disks[1].TargetPath != filepath.Join(normalizedRestoreDest, "data.img") {
		t.Fatalf("unexpected second target path: %q", plan.Disks[1].TargetPath)
	}

	if plan.NVRAMTargetPath != filepath.Join(normalizedRestoreDest, "Home_Assistant_VARS.fd") {
		t.Fatalf("unexpected NVRAM target path: %q", plan.NVRAMTargetPath)
	}

	if strings.Contains(plan.DomainXML, "/mnt/cache/domains/haos/haos.qcow2") {
		t.Fatalf("expected disk source path to be rewritten: %s", plan.DomainXML)
	}

	if !strings.Contains(plan.DomainXML, filepath.Join(normalizedRestoreDest, "haos.qcow2")) {
		t.Fatalf("expected rewritten disk path in XML: %s", plan.DomainXML)
	}

	if !strings.Contains(plan.DomainXML, filepath.Join(normalizedRestoreDest, "Home_Assistant_VARS.fd")) {
		t.Fatalf("expected rewritten NVRAM path in XML: %s", plan.DomainXML)
	}
}

func TestStripDomainBackingStores(t *testing.T) {
	t.Parallel()

	xmlDesc := `<domain>
	<metadata>
		<vmtemplate xmlns="unraid" name="Linux"></vmtemplate>
	</metadata>
  <devices>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
      <backingStore type="file">
        <format type="qcow2"></format>
        <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
        <backingStore/>
      </backingStore>
      <target dev="vda"></target>
    </disk>
  </devices>
</domain>`

	sanitized, err := stripDomainBackingStores(xmlDesc)
	if err != nil {
		t.Fatalf("stripDomainBackingStores() error = %v", err)
	}

	if strings.Contains(sanitized, "<backingStore") {
		t.Fatalf("expected backingStore sections to be removed: %s", sanitized)
	}

	if !strings.Contains(sanitized, `/mnt/cache/domains/haos/haos.qcow2`) {
		t.Fatalf("expected primary disk source to remain: %s", sanitized)
	}

	if strings.Count(sanitized, `xmlns="unraid"`) != 1 {
		t.Fatalf("expected namespace declarations to be preserved exactly once: %s", sanitized)
	}
}

func TestBuildVMRestorePlanStripsLegacyBackingStore(t *testing.T) {
	t.Parallel()

	xmlDesc := `<domain>
  <devices>
    <disk type="file" device="disk">
      <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
      <backingStore type="file">
        <source file="/mnt/cache/domains/haos/haos.qcow2"></source>
        <backingStore/>
      </backingStore>
      <target dev="vda"></target>
    </disk>
  </devices>
</domain>`

	plan, err := buildVMRestorePlan([]byte(xmlDesc), "")
	if err != nil {
		t.Fatalf("buildVMRestorePlan() error = %v", err)
	}

	if strings.Contains(plan.DomainXML, "<backingStore") {
		t.Fatalf("expected buildVMRestorePlan() to strip backingStore sections: %s", plan.DomainXML)
	}

	if len(plan.Disks) != 1 || plan.Disks[0].TargetPath != "/mnt/cache/domains/haos/haos.qcow2" {
		t.Fatalf("unexpected restore plan after stripping backingStore: %+v", plan.Disks)
	}
}

func TestBuildBackupXML(t *testing.T) {
	t.Parallel()

	xmlDesc, artifacts, err := buildBackupXML("/tmp/vault-backup", []domainDisk{
		{Index: 0, Path: "/mnt/cache/domains/haos/haos.qcow2", Target: "vda"},
		{Index: 1, Path: "/mnt/cache/domains/haos/data.img", Target: "vdb"},
	})
	if err != nil {
		t.Fatalf("buildBackupXML() error = %v", err)
	}

	if len(artifacts) != 2 {
		t.Fatalf("expected 2 artifacts, got %d", len(artifacts))
	}

	if artifacts[0].BackupFile != "vdisk0.qcow2" {
		t.Fatalf("unexpected first backup file: %q", artifacts[0].BackupFile)
	}

	if artifacts[1].BackupFile != "vdisk1.img" {
		t.Fatalf("unexpected second backup file: %q", artifacts[1].BackupFile)
	}

	if artifacts[0].TargetPath != filepath.Join("/tmp/vault-backup", "vdisk0.qcow2") {
		t.Fatalf("unexpected first target path: %q", artifacts[0].TargetPath)
	}

	if !strings.Contains(xmlDesc, `<disk name="vda" backup="yes" type="file">`) {
		t.Fatalf("expected backup XML to include vda disk entry: %s", xmlDesc)
	}

	if !strings.Contains(xmlDesc, `file="/tmp/vault-backup/vdisk0.qcow2"`) {
		t.Fatalf("expected backup XML to include vdisk0 target path: %s", xmlDesc)
	}

	if !strings.Contains(xmlDesc, `<driver type="qcow2"></driver>`) {
		t.Fatalf("expected backup XML to preserve qcow2 output type: %s", xmlDesc)
	}

	if !strings.Contains(xmlDesc, `<driver type="raw"></driver>`) {
		t.Fatalf("expected backup XML to use raw output for non-qcow disks: %s", xmlDesc)
	}
}

func TestVMEngineDoesNotShellOut(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("vm*.go")
	if err != nil {
		t.Fatalf("glob vm*.go: %v", err)
	}

	forbidden := []string{
		`exec.Command("virsh"`,
		`exec.LookPath("virsh"`,
		`qemu-img`,
		`copyOrFlattenDisk`,
	}

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}

		content := string(data)
		for _, needle := range forbidden {
			if strings.Contains(content, needle) {
				t.Fatalf("forbidden VM engine shell dependency %q found in %s", needle, file)
			}
		}
	}
}

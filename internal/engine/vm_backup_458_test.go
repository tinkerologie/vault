//go:build linux

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

// testDomainXML renders a minimal libvirt domain with one file-backed disk per
// path (targets vda, vdb, ...) and an optional NVRAM path.
func testDomainXML(driverType, nvram string, diskPaths ...string) string {
	var b strings.Builder
	b.WriteString("<domain type='kvm'><name>vm1</name>")
	if nvram != "" {
		fmt.Fprintf(&b, "<os><nvram>%s</nvram></os>", nvram)
	}
	b.WriteString("<devices>")
	for i, p := range diskPaths {
		fmt.Fprintf(&b, "<disk type='file' device='disk'><driver name='qemu' type='%s'/><source file='%s'/><target dev='vd%c' bus='virtio'/></disk>",
			driverType, p, 'a'+i)
	}
	b.WriteString("</devices></domain>")
	return b.String()
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readVMMeta(t *testing.T, dir string) vmBackupMetadata {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, vmMetadataFileName))
	if err != nil {
		t.Fatal(err)
	}
	var m vmBackupMetadata
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func xmlReturning(xml string) func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) {
	return func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) { return xml, nil }
}

func TestVMBackupColdShutoffFull458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	tmp := t.TempDir()
	disk := filepath.Join(tmp, "src", "vdisk1.img")
	nvram := filepath.Join(tmp, "src", "vm_VARS.fd")
	writeTestFile(t, disk, "diskdata")
	writeTestFile(t, nvram, "nvram")
	dest := filepath.Join(tmp, "dest")

	var backupXML string
	fake := &fakeLibvirt{
		DomainGetStateFn:   stateSeq(libvirt.DomainShutoff),
		DomainGetXMLDescFn: xmlReturning(testDomainXML("raw", nvram, disk)),
		DomainBackupBeginFn: func(_ libvirt.Domain, xml string) error {
			backupXML = xml
			writeTestFile(t, filepath.Join(dest, "vdisk0.img"), "diskdata")
			return nil
		},
	}
	h := &VMHandler{conn: fake}
	progress := &progressLog{}
	item := BackupItem{Name: "vm1", Settings: map[string]any{"backup_run_id": int64(42)}}

	res, err := h.Backup(context.Background(), item, dest, progress.fn)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if !res.Success || res.Meta["vm_checkpoint"] != "vault-run-42" || res.Meta["vm_backup_type"] != "full" {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := res.Meta["vm_parent_checkpoint"]; ok {
		t.Fatal("full backup reported a parent checkpoint")
	}
	var names []string
	for _, f := range res.Files {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, ","); got != "domain.xml,vm_meta.json,vdisk0.img,vm_VARS.fd" {
		t.Fatalf("files = %s", got)
	}
	if strings.Contains(backupXML, "<incremental>") || !strings.Contains(backupXML, `type="raw"`) {
		t.Fatalf("backup XML = %s", backupXML)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "vm_VARS.fd")); string(data) != "nvram" {
		t.Fatalf("NVRAM copy = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "domain.xml")); !strings.Contains(string(data), disk) {
		t.Fatalf("domain.xml = %s", data)
	}
	meta := readVMMeta(t, dest)
	if meta.BackupType != "full" || meta.Checkpoint != "vault-run-42" || meta.State != "shutoff" ||
		len(meta.Disks) != 1 || meta.Disks[0].Target != "vda" || meta.Disks[0].BackupFile != "vdisk0.img" || meta.Disks[0].Format != "raw" {
		t.Fatalf("metadata = %+v", meta)
	}
	for name, want := range map[string]int{
		"DomainCreateWithFlags": 1, "DomainBackupBegin": 1, "DomainDestroy": 1, "DomainCheckpointCreateXML": 1,
	} {
		if got := fake.count(name); got != want {
			t.Errorf("%s calls = %d, want %d", name, got, want)
		}
	}
	for _, msg := range []string{"starting domain paused for backup", "copying NVRAM", "backup complete"} {
		if !progress.has(msg) {
			t.Errorf("missing progress %q", msg)
		}
	}
}

func TestVMBackupIncrementalSnapshot458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	tmp := t.TempDir()
	disk := filepath.Join(tmp, "src", "vdisk1.qcow2")
	writeTestFile(t, disk, "qcow")
	dest := filepath.Join(tmp, "dest")

	var backupXML string
	var liveXMLRequested bool
	fake := &fakeLibvirt{
		DomainGetXMLDescFn: func(_ libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error) {
			if flags == libvirt.DomainXMLSecure {
				liveXMLRequested = true
			}
			return testDomainXML("qcow2", "", disk), nil
		},
		DomainBackupBeginFn: func(_ libvirt.Domain, xml string) error { backupXML = xml; return nil },
	}
	h := &VMHandler{conn: fake}
	item := BackupItem{Name: "vm1", Settings: map[string]any{
		"backup_type":       "incremental",
		"parent_checkpoint": "vault-run-1",
		"backup_run_id":     2,
	}}
	res, err := h.Backup(context.Background(), item, dest, noProgress)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if res.Meta["vm_backup_type"] != "incremental" || res.Meta["vm_parent_checkpoint"] != "vault-run-1" || res.Meta["vm_checkpoint"] != "vault-run-2" {
		t.Fatalf("meta = %+v", res.Meta)
	}
	if !liveXMLRequested {
		t.Fatal("running domain backup did not read the live XML")
	}
	if !strings.Contains(backupXML, "<incremental>vault-run-1</incremental>") || !strings.Contains(backupXML, "vdisk0.qcow2") {
		t.Fatalf("backup XML = %s", backupXML)
	}
	if fake.count("DomainCreateWithFlags") != 0 || fake.count("DomainDestroy") != 0 || fake.count("DomainShutdownFlags") != 0 {
		t.Fatalf("snapshot-mode backup touched power state: %v", fake.calls)
	}
	meta := readVMMeta(t, dest)
	if meta.BackupType != "incremental" || meta.ParentCheckpoint != "vault-run-1" || meta.Disks[0].Format != "qcow2" {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestVMBackupFallsBackToFull458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	tests := []struct {
		name     string
		driver   string
		ext      string
		parent   string
		cpLookup error
	}{
		{name: "no parent recorded", driver: "qcow2", ext: ".qcow2"},
		{name: "parent checkpoint vanished", driver: "qcow2", ext: ".qcow2", parent: "vault-run-1", cpLookup: errors.New("no checkpoint")},
		{name: "raw disks", driver: "raw", ext: ".img", parent: "vault-run-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			disk := filepath.Join(tmp, "src", "vdisk1"+tt.ext)
			writeTestFile(t, disk, "x")
			var backupXML string
			fake := &fakeLibvirt{
				DomainGetXMLDescFn:  xmlReturning(testDomainXML(tt.driver, "", disk)),
				DomainBackupBeginFn: func(_ libvirt.Domain, xml string) error { backupXML = xml; return nil },
			}
			if tt.cpLookup != nil {
				fake.DomainCheckpointLookupByNameFn = func(libvirt.Domain, string) (libvirt.DomainCheckpoint, error) {
					return libvirt.DomainCheckpoint{}, tt.cpLookup
				}
			}
			item := BackupItem{Name: "vm1", Settings: map[string]any{"backup_type": "differential", "parent_checkpoint": tt.parent}}
			res, err := (&VMHandler{conn: fake}).Backup(context.Background(), item, filepath.Join(tmp, "dest"), noProgress)
			if err != nil {
				t.Fatalf("Backup: %v", err)
			}
			if res.Meta["vm_backup_type"] != "full" {
				t.Fatalf("type = %v, want full", res.Meta["vm_backup_type"])
			}
			if _, ok := res.Meta["vm_parent_checkpoint"]; ok {
				t.Fatal("fallback still reports a parent checkpoint")
			}
			if strings.Contains(backupXML, "<incremental>") {
				t.Fatalf("fallback backup XML is incremental: %s", backupXML)
			}
		})
	}
}

func TestVMBackupColdRunning458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	tmp := t.TempDir()
	disk := filepath.Join(tmp, "src", "vdisk1.img")
	writeTestFile(t, disk, "x")
	fake := &fakeLibvirt{
		// Backup reads running; the shutdown wait and the restart check see shutoff.
		DomainGetStateFn:   stateSeq(libvirt.DomainRunning, libvirt.DomainShutoff),
		DomainGetXMLDescFn: xmlReturning(testDomainXML("raw", "", disk)),
	}
	item := BackupItem{Name: "vm1", Settings: map[string]any{"backup_mode": "cold"}}
	res, err := (&VMHandler{conn: fake}).Backup(context.Background(), item, filepath.Join(tmp, "dest"), noProgress)
	if err != nil || !res.Success {
		t.Fatalf("Backup = %+v, %v", res, err)
	}
	for name, want := range map[string]int{
		"DomainShutdownFlags": 1, "DomainCreateWithFlags": 1, "DomainDestroy": 1, "DomainCreate": 1,
	} {
		if got := fake.count(name); got != want {
			t.Errorf("%s calls = %d, want %d", name, got, want)
		}
	}
}

func TestVMBackupNoDisksSelected458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	tmp := t.TempDir()
	disk := filepath.Join(tmp, "src", "vdisk1.img")
	writeTestFile(t, disk, "x")

	tests := []struct {
		name     string
		xml      string
		settings map[string]any
	}{
		{name: "domain without disks", xml: testDomainXML("raw", ""), settings: map[string]any{}},
		{name: "nothing changed since", xml: testDomainXML("raw", "", disk), settings: map[string]any{
			"changed_since": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeLibvirt{DomainGetXMLDescFn: xmlReturning(tt.xml)}
			progress := &progressLog{}
			res, err := (&VMHandler{conn: fake}).Backup(context.Background(), BackupItem{Name: "vm1", Settings: tt.settings}, t.TempDir(), progress.fn)
			if err != nil {
				t.Fatalf("Backup: %v", err)
			}
			if res.Meta["vm_checkpoint"] != "" || !progress.has("no VM disks selected for backup") {
				t.Fatalf("meta=%+v", res.Meta)
			}
			if fake.count("DomainBackupBegin") != 0 {
				t.Fatal("backup job started with no disks")
			}
		})
	}
}

func TestVMBackupCheckpointFailureIsWarning458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	tmp := t.TempDir()
	disk := filepath.Join(tmp, "src", "vdisk1.img")
	writeTestFile(t, disk, "x")
	fake := &fakeLibvirt{
		DomainGetXMLDescFn: xmlReturning(testDomainXML("raw", "", disk)),
		DomainCheckpointCreateXMLFn: func(libvirt.Domain, string) (libvirt.DomainCheckpoint, error) {
			return libvirt.DomainCheckpoint{}, errors.New("no bitmaps")
		},
	}
	res, err := (&VMHandler{conn: fake}).Backup(context.Background(), BackupItem{Name: "vm1", Settings: map[string]any{}}, filepath.Join(tmp, "dest"), noProgress)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if res.Meta["vm_checkpoint"] != "" {
		t.Fatalf("checkpoint = %v, want empty after failure", res.Meta["vm_checkpoint"])
	}
}

func TestVMBackupErrors458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	boom := errors.New("boom")
	tmp := t.TempDir()
	disk := filepath.Join(tmp, "src", "vdisk1.img")
	writeTestFile(t, disk, "x")
	goodXML := testDomainXML("raw", "", disk)
	blockOnlyXML := "<domain><devices><disk type='block' device='disk'><source dev='/dev/sdb'/><target dev='vdb'/></disk></devices></domain>"
	notADir := filepath.Join(tmp, "file")
	writeTestFile(t, notADir, "x")

	secureInactive := libvirt.DomainXMLSecure | libvirt.DomainXMLInactive

	tests := []struct {
		name     string
		fake     func() *fakeLibvirt
		settings map[string]any
		dest     string
		want     string
	}{
		{
			name: "dest dir cannot be created",
			fake: func() *fakeLibvirt { return &fakeLibvirt{} },
			dest: filepath.Join(notADir, "sub"),
			want: "creating dest dir",
		},
		{
			name: "lookup",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainLookupByNameFn: func(string) (libvirt.Domain, error) { return libvirt.Domain{}, boom }}
			},
			want: "looking up domain vm1: boom",
		},
		{
			name: "state",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainGetStateFn: func(libvirt.Domain) (int32, int32, error) { return 0, 0, boom }}
			},
			want: "getting domain state: boom",
		},
		{
			name: "inactive xml",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainGetXMLDescFn: func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) { return "", boom }}
			},
			want: "getting inactive domain XML: boom",
		},
		{
			name: "live xml",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainGetXMLDescFn: func(_ libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error) {
					if flags == libvirt.DomainXMLSecure {
						return "", boom
					}
					return goodXML, nil
				}}
			},
			want: "getting live domain XML: boom",
		},
		{
			name: "unparsable xml",
			fake: func() *fakeLibvirt { return &fakeLibvirt{DomainGetXMLDescFn: xmlReturning("<domain")} },
			want: "parsing domain XML",
		},
		{
			name: "only block disks",
			fake: func() *fakeLibvirt { return &fakeLibvirt{DomainGetXMLDescFn: xmlReturning(blockOnlyXML)} },
			want: "has no file-backed disks the backup job can handle; skipped disks: [vdb(source type block, /dev/sdb)]",
		},
		{
			name: "saved xml fails",
			fake: func() *fakeLibvirt {
				n := 0
				return &fakeLibvirt{DomainGetXMLDescFn: func(_ libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error) {
					if flags == secureInactive {
						n++
						if n == 2 {
							return "", boom
						}
					}
					return goodXML, nil
				}}
			},
			want: "getting inactive domain XML: boom",
		},
		{
			name: "saved xml cannot be sanitized",
			fake: func() *fakeLibvirt {
				n := 0
				return &fakeLibvirt{DomainGetXMLDescFn: func(_ libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error) {
					if flags == secureInactive {
						n++
						if n == 2 {
							return "<domain><backingStore>", nil
						}
					}
					return goodXML, nil
				}}
			},
			want: "sanitizing domain XML: unterminated backingStore section",
		},
		{
			name:     "bad restore verify settings",
			fake:     func() *fakeLibvirt { return &fakeLibvirt{DomainGetXMLDescFn: xmlReturning(goodXML)} },
			settings: map[string]any{"restore_verify_mode": "bogus"},
			want:     "writing VM metadata",
		},
		{
			name: "changed-since filter error",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainGetXMLDescFn: xmlReturning(testDomainXML("raw", "", filepath.Join(tmp, "missing.img")))}
			},
			settings: map[string]any{"changed_since": time.Now().UTC().Format(time.RFC3339)},
			want:     "filtering changed disks",
		},
		{
			name: "cold start fails",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainGetStateFn:   stateSeq(libvirt.DomainShutoff),
					DomainGetXMLDescFn: xmlReturning(goodXML),
					DomainCreateWithFlagsFn: func(libvirt.Domain, uint32) (libvirt.Domain, error) {
						return libvirt.Domain{}, boom
					},
				}
			},
			want: "starting domain paused for backup: boom",
		},
		{
			name: "backup job fails and guest restored",
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainGetStateFn:    stateSeq(libvirt.DomainShutoff),
					DomainGetXMLDescFn:  xmlReturning(goodXML),
					DomainBackupBeginFn: func(libvirt.Domain, string) error { return boom },
				}
			},
			want: "starting backup job: boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := tt.dest
			if dest == "" {
				dest = filepath.Join(t.TempDir(), "dest")
			}
			settings := tt.settings
			if settings == nil {
				settings = map[string]any{}
			}
			res, err := (&VMHandler{conn: tt.fake()}).Backup(context.Background(), BackupItem{Name: "vm1", Settings: settings}, dest, noProgress)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Backup = %+v, %v; want error containing %q", res, err, tt.want)
			}
		})
	}
}

func TestVMBackupRestoreStateFailures458(t *testing.T) {
	fastVMPolling(t, 5*time.Millisecond)
	tmp := t.TempDir()
	disk := filepath.Join(tmp, "src", "vdisk1.img")
	writeTestFile(t, disk, "x")
	busy := errors.New("busy")

	t.Run("job failure plus teardown failure", func(t *testing.T) {
		fake := &fakeLibvirt{
			DomainGetStateFn:    stateSeq(libvirt.DomainShutoff, libvirt.DomainRunning),
			DomainGetXMLDescFn:  xmlReturning(testDomainXML("raw", "", disk)),
			DomainBackupBeginFn: func(libvirt.Domain, string) error { return errors.New("boom") },
			DomainDestroyFn:     func(libvirt.Domain) error { return busy },
		}
		_, err := (&VMHandler{conn: fake}).Backup(context.Background(), BackupItem{Name: "vm1", Settings: map[string]any{}}, filepath.Join(t.TempDir(), "d"), noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "starting backup job: boom; restoring domain state: destroying domain vm1: giving up") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("teardown failure after successful job", func(t *testing.T) {
		fake := &fakeLibvirt{
			DomainGetStateFn:   stateSeq(libvirt.DomainShutoff, libvirt.DomainRunning),
			DomainGetXMLDescFn: xmlReturning(testDomainXML("raw", "", disk)),
			DomainDestroyFn:    func(libvirt.Domain) error { return busy },
		}
		_, err := (&VMHandler{conn: fake}).Backup(context.Background(), BackupItem{Name: "vm1", Settings: map[string]any{}}, filepath.Join(t.TempDir(), "d"), noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "restoring domain state: destroying domain vm1: giving up") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestVMBackupRefreshAfterStaleCleanupErrors458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	tests := []struct {
		name    string
		refresh func() (string, error)
		want    string
	}{
		{name: "refresh fails", refresh: func() (string, error) { return "", errors.New("boom") }, want: "refreshing domain XML after cleanup: boom"},
		{name: "refresh unparsable", refresh: func() (string, error) { return "<domain", nil }, want: "re-parsing live domain XML after cleanup"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			base := filepath.Join(tmp, "vdisk1.img")
			writeTestFile(t, base, "base")
			current := testDomainXML("raw", "", base+".snap")
			secureCalls := 0
			fake := &fakeLibvirt{
				// Running domain: the first Secure-only read is the live XML,
				// the second is the post-cleanup refresh under test.
				DomainGetXMLDescFn: func(_ libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error) {
					if flags == libvirt.DomainXMLSecure {
						secureCalls++
						if secureCalls == 2 {
							return tt.refresh()
						}
					}
					return current, nil
				},
				DomainDefineXMLFlagsFn: func(xml string) (libvirt.Domain, error) { current = xml; return libvirt.Domain{}, nil },
			}
			_, err := (&VMHandler{conn: fake}).Backup(context.Background(), BackupItem{Name: "vm1", Settings: map[string]any{}}, filepath.Join(tmp, "dest"), noProgress)
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Fatalf("err = %v, want prefix %q", err, tt.want)
			}
		})
	}
}

func TestVMBackupStagingSpacePreflight458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	tmp := t.TempDir()
	disk := filepath.Join(tmp, "huge.img")
	f, err := os.Create(disk)
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file with an absurd apparent size: no real disk is consumed,
	// but the preflight (which uses apparent size) must refuse it.
	if err := f.Truncate(1 << 43); err != nil {
		_ = f.Close()
		t.Skipf("filesystem cannot hold an 8 TiB sparse file: %v", err)
	}
	_ = f.Close()

	if err := checkStagingSpaceForDisks(tmp, "vm1", []domainDisk{{Path: disk}}); err == nil || !strings.Contains(err.Error(), "VM vm1 needs ~8192 GiB of staging space") {
		t.Fatalf("preflight = %v", err)
	}

	fake := &fakeLibvirt{DomainGetXMLDescFn: xmlReturning(testDomainXML("raw", "", disk))}
	_, err = (&VMHandler{conn: fake}).Backup(context.Background(), BackupItem{Name: "vm1", Settings: map[string]any{}}, filepath.Join(tmp, "dest"), noProgress)
	if err == nil || !strings.Contains(err.Error(), "staging space") {
		t.Fatalf("Backup = %v, want staging space error", err)
	}
}

func TestCheckStagingSpaceForDisksPasses458(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	small := filepath.Join(tmp, "small.img")
	writeTestFile(t, small, "tiny")
	if err := checkStagingSpaceForDisks(tmp, "vm1", nil); err != nil {
		t.Fatalf("no disks: %v", err)
	}
	if err := checkStagingSpaceForDisks(tmp, "vm1", []domainDisk{{Path: filepath.Join(tmp, "missing")}}); err != nil {
		t.Fatalf("missing disk: %v", err)
	}
	if err := checkStagingSpaceForDisks(tmp, "vm1", []domainDisk{{Path: small}}); err != nil {
		t.Fatalf("small disk: %v", err)
	}
	if err := checkStagingSpaceForDisks(filepath.Join(tmp, "nope"), "vm1", []domainDisk{{Path: small}}); err != nil {
		t.Fatalf("statfs failure should be advisory: %v", err)
	}
}

func TestVMListItems458(t *testing.T) {
	t.Parallel()

	t.Run("list error", func(t *testing.T) {
		t.Parallel()
		h := &VMHandler{conn: &fakeLibvirt{ConnectListAllDomainsFn: func() ([]libvirt.Domain, uint32, error) {
			return nil, 0, errors.New("rpc")
		}}}
		if _, err := h.ListItems(); err == nil || err.Error() != "listing domains: rpc" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("items", func(t *testing.T) {
		t.Parallel()
		fake := &fakeLibvirt{
			ConnectListAllDomainsFn: func() ([]libvirt.Domain, uint32, error) {
				return []libvirt.Domain{{Name: "qcow"}, {Name: "noxml"}, {Name: "badxml"}}, 3, nil
			},
			DomainGetStateFn: func(dom libvirt.Domain) (int32, int32, error) {
				if dom.Name == "qcow" {
					return int32(libvirt.DomainRunning), 0, nil
				}
				return int32(libvirt.DomainShutoff), 0, nil
			},
			DomainGetXMLDescFn: func(dom libvirt.Domain, _ libvirt.DomainXMLFlags) (string, error) {
				switch dom.Name {
				case "qcow":
					return testDomainXML("qcow2", "", "/mnt/user/domains/qcow/vdisk1.img"), nil
				case "noxml":
					return "", errors.New("rpc")
				default:
					return "<domain", nil
				}
			},
		}
		items, err := (&VMHandler{conn: fake}).ListItems()
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 3 {
			t.Fatalf("items = %d", len(items))
		}
		want := []struct {
			state, format string
			incr          bool
		}{
			{"running", "qcow2", true},
			{"shutoff", "unknown", false},
			{"shutoff", "unknown", false},
		}
		for i, w := range want {
			s := items[i].Settings
			if items[i].Type != "vm" || s["state"] != w.state || s["disk_format"] != w.format || s["supports_incremental"] != w.incr {
				t.Errorf("item %d = %+v", i, items[i])
			}
			if s["uuid"] != "00000000-0000-0000-0000-000000000000" {
				t.Errorf("item %d uuid = %v", i, s["uuid"])
			}
		}
	})
}

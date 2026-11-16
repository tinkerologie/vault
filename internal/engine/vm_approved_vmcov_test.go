package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	vmcovUUID = "22222222-3333-4444-aaaa-555555555555"
	vmcovDisk = `<disk type="file" device="disk"><driver type="raw"/><source file="/mnt/user/domains/fixture/disk.img"/><target dev="vda"/></disk>`
	vmcovMeta = `{"backup_type":"full","state":"shutoff","disks":[{"target":"vda","backup_file":"vdisk0.img","format":"raw"}]}`
)

func vmcovDomain(name, uuid, os, devices string) []byte {
	return []byte(`<domain><name>` + name + `</name><uuid>` + uuid + `</uuid>` + os + `<devices>` + devices + `</devices></domain>`)
}

func TestVMCovRestoreContextValues(t *testing.T) {
	var result VMReadinessResult
	result.Startup = "stale"
	ctx := WithVMReadinessResult(context.Background(), &result)
	if result != (VMReadinessResult{Startup: "not_attempted", Running: "not_attempted", CleanupStop: "not_attempted", ApplicationHealth: "unchecked"}) {
		t.Fatalf("readiness was not reset: %+v", result)
	}
	if got, _ := ctx.Value(vmReadinessKey{}).(*VMReadinessResult); got != &result {
		t.Fatal("readiness result is not carried by the context")
	}
	ctx = WithVMRestoreExpectation(ctx, []byte("key"), "digest")
	got, ok := ctx.Value(vmExpectationKey{}).(vmRestoreExpectation)
	if !ok || string(got.Key) != "key" || got.Digest != "digest" {
		t.Fatalf("expectation = %+v, %v", got, ok)
	}
}

func TestVMCovInspectVMBackupRefusesUnconfirmedInventory(t *testing.T) {
	files := []string{"vdisk0.img", "vars.fd"}
	nvram := `<os><nvram>/etc/libvirt/qemu/nvram/vars.fd</nvram></os>`
	for _, tt := range []struct {
		name     string
		xml      []byte
		metadata string
		files    []string
		want     error
	}{
		{name: "malformed XML", xml: []byte("<domain>"), want: ErrVMBackupEvidenceUnknown},
		{name: "two names", xml: []byte(`<domain><name>a</name><name>b</name><uuid>` + vmcovUUID + `</uuid></domain>`), want: ErrVMBackupEvidenceUnknown},
		{name: "empty name", xml: vmcovDomain("", vmcovUUID, "", vmcovDisk), want: ErrVMBackupEvidenceUnknown},
		{name: "short UUID", xml: vmcovDomain("fixture", "1234", "", vmcovDisk), want: ErrVMBackupEvidenceUnknown},
		{name: "non-hex UUID", xml: vmcovDomain("fixture", "zzzzzzzz-3333-4444-aaaa-555555555555", "", vmcovDisk), want: ErrVMBackupEvidenceUnknown},
		{name: "unreadable metadata", xml: vmcovDomain("fixture", vmcovUUID, "", vmcovDisk), metadata: "{", want: ErrVMBackupEvidenceUnknown},
		{name: "NVRAM source element", xml: vmcovDomain("fixture", vmcovUUID, `<os><nvram><source file="/x"/></nvram></os>`, vmcovDisk), want: ErrVMBackupEvidenceUnknown},
		{name: "two NVRAM files", xml: vmcovDomain("fixture", vmcovUUID, `<os><nvram>/a.fd</nvram><nvram>/b.fd</nvram></os>`, vmcovDisk), want: ErrVMBackupEvidenceUnknown},
		{name: "NVRAM named like a sidecar", xml: vmcovDomain("fixture", vmcovUUID, `<os><nvram>/etc/libvirt/qemu/nvram/domain.xml</nvram></os>`, vmcovDisk), want: ErrVMBackupEvidenceUnknown},
		{name: "disk count differs", xml: vmcovDomain("fixture", vmcovUUID, "", vmcovDisk+strings.ReplaceAll(vmcovDisk, "vda", "vdb")), want: ErrVMBackupEvidenceUnknown},
		{name: "unsafe target name", xml: vmcovDomain("fixture", vmcovUUID, "", strings.ReplaceAll(vmcovDisk, `dev="vda"`, `dev="v/a"`)),
			metadata: strings.ReplaceAll(vmcovMeta, `"vda"`, `"v/a"`), want: ErrVMBackupEvidenceUnknown},
		{name: "unrecorded target", xml: vmcovDomain("fixture", vmcovUUID, "", vmcovDisk), metadata: strings.ReplaceAll(vmcovMeta, `"vda"`, `"vdb"`), want: ErrVMBackupEvidenceUnknown},
		{name: "format differs", xml: vmcovDomain("fixture", vmcovUUID, "", vmcovDisk), metadata: strings.ReplaceAll(vmcovMeta, `"format":"raw"`, `"format":"qcow2"`), want: ErrVMBackupEvidenceUnknown},
		{name: "unsupported format", xml: vmcovDomain("fixture", vmcovUUID, "", strings.ReplaceAll(vmcovDisk, `type="raw"`, `type="vmdk"`)),
			metadata: strings.ReplaceAll(vmcovMeta, `"format":"raw"`, `"format":"vmdk"`), want: ErrVMBackupEvidenceUnknown},
		{name: "disk reuses a sidecar name", xml: vmcovDomain("fixture", vmcovUUID, "", vmcovDisk), metadata: strings.ReplaceAll(vmcovMeta, "vdisk0.img", "vm_meta.json"), want: ErrVMBackupEvidenceUnknown},
		{name: "NVRAM artifact missing", xml: vmcovDomain("fixture", vmcovUUID, nvram, vmcovDisk), files: []string{"vdisk0.img"}, want: ErrVMBackupArtifactMissing},
		{name: "recorded with NVRAM", xml: vmcovDomain("Fixture", strings.ToUpper(vmcovUUID), nvram, vmcovDisk)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			metadata, list := tt.metadata, tt.files
			if metadata == "" {
				metadata = vmcovMeta
			}
			if list == nil {
				list = files
			}
			evidence, err := InspectVMBackup(tt.xml, []byte(metadata), list)
			if !errors.Is(err, tt.want) {
				t.Fatalf("InspectVMBackup error = %v, want %v", err, tt.want)
			}
			if tt.want == nil && (evidence.UUID != vmcovUUID || evidence.NVRAMFile != "vars.fd" || evidence.Name != "Fixture") {
				t.Fatalf("evidence = %+v", evidence)
			}
		})
	}
}

func TestVMCovValidVMTargetName(t *testing.T) {
	for target, want := range map[string]bool{
		"":                      false,
		strings.Repeat("a", 65): false,
		strings.Repeat("a", 64): true,
		"sda_1-x.Y":             true,
		"vd a":                  false,
		"vda/../../etc":         false,
	} {
		if got := validVMTargetName(target); got != want {
			t.Errorf("validVMTargetName(%q) = %v, want %v", target, got, want)
		}
	}
}

func TestVMCovBuildApprovedVMPlan(t *testing.T) {
	nvram := `<os><loader>/usr/share/qemu/ovmf.fd</loader><nvram>/etc/libvirt/qemu/nvram/vars.fd</nvram></os>`
	blockDisk := `<disk type="block" device="disk"><source dev="/dev/sdz"/><target dev="sdz"/></disk>` +
		`<disk type="mystery" device="disk"><target dev="bad target"/></disk>` +
		`<disk type="file" device="cdrom"><source file="/mnt/user/isos/x.iso"/><target dev="hdc"/></disk>`
	files := []string{"vdisk0.img", "vars.fd"}

	t.Run("original locations", func(t *testing.T) {
		meta := `{"backup_type":"full","state":"running","restore_verify":{"mode":"tcp","tcp_port":22},"disks":[{"target":"vda","backup_file":"vdisk0.img","format":"raw"}]}`
		got, err := buildApprovedVMPlan("fixture", "", vmcovDomain("fixture", vmcovUUID, nvram, vmcovDisk+blockDisk), []byte(meta), files)
		if err != nil {
			t.Fatalf("buildApprovedVMPlan: %v", err)
		}
		effects := got.Effects
		if effects.Destination != "" || len(effects.Disks) != 1 || effects.Disks[0].Destination != "/mnt/user/domains/fixture/disk.img" ||
			effects.Disks[0].Target != "vda" || effects.Disks[0].Format != "raw" || effects.NVRAM == nil ||
			!strings.HasSuffix(effects.NVRAM.Destination, "/etc/libvirt/qemu/nvram/vars.fd") {
			t.Fatalf("effects = %+v", effects)
		}
		if effects.ReadinessMode != "tcp" || effects.ReadinessAddress != "automatic" || effects.ReadinessPort != 22 {
			t.Fatalf("readiness = %+v", effects)
		}
		wantSkipped := []VMUnsupportedDisk{{"sdz", "block"}, {"unknown", "unknown"}}
		if len(effects.UnsupportedDisks) != len(wantSkipped) || effects.UnsupportedDisks[0] != wantSkipped[0] || effects.UnsupportedDisks[1] != wantSkipped[1] {
			t.Fatalf("unsupported disks = %+v", effects.UnsupportedDisks)
		}
		if len(got.UnresolvedTargets) != 2 || got.Plan.Disks[0].BackupFile != "vdisk0.img" {
			t.Fatalf("plan = %+v, unresolved = %v", got.Plan, got.UnresolvedTargets)
		}
		if !strings.Contains(got.Plan.DomainXML, "/mnt/user/domains/fixture/disk.img") {
			t.Fatalf("domain XML lost the original disk path: %s", got.Plan.DomainXML)
		}
	})

	t.Run("custom destination rewrites disk and firmware", func(t *testing.T) {
		got, err := buildApprovedVMPlan("fixture", "/mnt/user/restored", vmcovDomain("fixture", vmcovUUID, nvram, vmcovDisk), []byte(vmcovMeta), files)
		if err != nil {
			t.Fatalf("buildApprovedVMPlan: %v", err)
		}
		if got.Effects.Destination != "/mnt/user/restored" || got.Effects.Disks[0].Destination != "/mnt/user/restored/disk.img" ||
			got.Effects.NVRAM.Destination != "/mnt/user/restored/vars.fd" {
			t.Fatalf("effects = %+v", got.Effects)
		}
		if strings.Contains(got.Plan.DomainXML, "/mnt/user/domains/fixture/disk.img") || strings.Contains(got.Plan.DomainXML, "/etc/libvirt/qemu/nvram/vars.fd") ||
			!strings.Contains(got.Plan.DomainXML, `file="/mnt/user/restored/disk.img"`) || !strings.Contains(got.Plan.DomainXML, "<nvram>/mnt/user/restored/vars.fd</nvram>") {
			t.Fatalf("domain XML was not rewritten: %s", got.Plan.DomainXML)
		}
	})

	t.Run("explicit readiness address", func(t *testing.T) {
		meta := `{"backup_type":"full","state":"shutoff","restore_verify":{"mode":"tcp","tcp_host":"host.invalid","tcp_port":80},"disks":[{"target":"vda","backup_file":"vdisk0.img","format":"raw"}]}`
		got, err := buildApprovedVMPlan("fixture", "", vmcovDomain("fixture", vmcovUUID, "", vmcovDisk), []byte(meta), files)
		if err != nil || got.Effects.ReadinessAddress != "explicit" {
			t.Fatalf("effects = %+v, err = %v", got.Effects, err)
		}
	})

	twoDisks := vmcovDisk + strings.ReplaceAll(strings.ReplaceAll(vmcovDisk, "vda", "vdb"), "disk.img", "other.img")
	twoMeta := `{"backup_type":"full","state":"shutoff","disks":[{"target":"vda","backup_file":"vdisk0.img","format":"raw"},{"target":"vdb","backup_file":"vdisk1.img","format":"raw"}]}`
	for _, tt := range []struct {
		name, vmName, destination string
		xml                       []byte
		metadata                  string
		files                     []string
		want                      error
	}{
		{name: "bad evidence", vmName: "fixture", xml: []byte("<domain/>"), want: ErrVMBackupEvidenceUnknown},
		{name: "different VM", vmName: "other", xml: vmcovDomain("fixture", vmcovUUID, "", vmcovDisk), want: ErrVMRestoreIdentityChanged},
		{name: "destination outside policy", vmName: "fixture", destination: "/root/restore", xml: vmcovDomain("fixture", vmcovUUID, "", vmcovDisk), want: ErrVMRestoreTargetDisallowed},
		{name: "destination traversal", vmName: "fixture", destination: "/mnt/user/../../root", xml: vmcovDomain("fixture", vmcovUUID, "", vmcovDisk), want: ErrVMRestoreTargetDisallowed},
		{name: "disk outside policy", vmName: "fixture", xml: vmcovDomain("fixture", vmcovUUID, "", strings.ReplaceAll(vmcovDisk, "/mnt/user/domains/fixture", "/root")), want: ErrVMRestoreTargetDisallowed},
		{name: "disks collide at destination", vmName: "fixture", destination: "/mnt/user/restored",
			xml: vmcovDomain("fixture", vmcovUUID, "", vmcovDisk+strings.ReplaceAll(vmcovDisk, "vda", "vdb")), metadata: twoMeta,
			files: []string{"vdisk0.img", "vdisk1.img"}, want: ErrVMRestoreTargetCollision},
		{name: "firmware collides with disk", vmName: "fixture", destination: "/mnt/user/restored",
			xml: vmcovDomain("fixture", vmcovUUID, `<os><nvram>/etc/libvirt/qemu/nvram/disk.img</nvram></os>`, twoDisks), metadata: twoMeta,
			files: []string{"vdisk0.img", "vdisk1.img", "disk.img"}, want: ErrVMRestoreTargetCollision},
	} {
		t.Run(tt.name, func(t *testing.T) {
			metadata, list := tt.metadata, tt.files
			if metadata == "" {
				metadata = vmcovMeta
			}
			if list == nil {
				list = files
			}
			if _, err := buildApprovedVMPlan(tt.vmName, tt.destination, tt.xml, []byte(metadata), list); !errors.Is(err, tt.want) {
				t.Fatalf("buildApprovedVMPlan error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestVMCovRewriteApprovedVMXML(t *testing.T) {
	doc := `<domain xmlns:q="urn:x"><os><nvram>/old/vars.fd</nvram></os><devices>` +
		`<disk type="file" device="disk"><source q:x="1" file='/old/a&amp;b.img' index="2"/></disk>` +
		`<disk type="file" device="cdrom"><source file="/isos/x.iso"/></disk></devices><!-- note --></domain>`
	got, err := rewriteApprovedVMXML(doc, map[string]string{"/old/a&b.img": "/new/<a>.img", "/old/vars.fd": "/new/vars.fd"})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	want := `<domain xmlns:q="urn:x"><os><nvram>/new/vars.fd</nvram></os><devices>` +
		`<disk type="file" device="disk"><source q:x="1" file='/new/&lt;a&gt;.img' index="2"/></disk>` +
		`<disk type="file" device="cdrom"><source file="/isos/x.iso"/></disk></devices><!-- note --></domain>`
	if got != want {
		t.Fatalf("rewrite =\n%s\nwant\n%s", got, want)
	}

	unchanged := `<domain><os><nvram>/same/vars.fd</nvram></os><devices><disk device="disk"><source file="/same/a.img"/></disk></devices></domain>`
	if got, err := rewriteApprovedVMXML(unchanged, map[string]string{"/same/a.img": "/same/a.img", "/same/vars.fd": "/same/vars.fd"}); err != nil || got != unchanged {
		t.Fatalf("identity rewrite = %q, %v", got, err)
	}

	for name, tt := range map[string]struct {
		doc          string
		replacements map[string]string
	}{
		"malformed":            {doc: `<domain><devices>`, replacements: map[string]string{}},
		"unreviewed disk path": {doc: unchanged, replacements: map[string]string{"/same/vars.fd": "/same/vars.fd"}},
		"reviewed path absent": {doc: unchanged, replacements: map[string]string{"/same/a.img": "/x.img", "/same/vars.fd": "/v.fd", "/gone.img": "/y.img"}},
	} {
		if _, err := rewriteApprovedVMXML(tt.doc, tt.replacements); !errors.Is(err, ErrVMBackupEvidenceUnknown) {
			t.Errorf("%s: error = %v, want %v", name, err, ErrVMBackupEvidenceUnknown)
		}
	}
}

func TestVMCovConfineVMReviewTargetRequiresContainingRoot(t *testing.T) {
	// The root list is matched after cleaning, so a trailing separator still
	// reports the cleaned root.
	got, root, err := confineVMReviewTarget("/mnt/user/disk.img", []string{"/mnt/"})
	if err != nil || got != "/mnt/user/disk.img" || root != "/mnt" {
		t.Fatalf("confineVMReviewTarget = %q, %q, %v", got, root, err)
	}
}

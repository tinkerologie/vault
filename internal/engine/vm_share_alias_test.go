package engine

import (
	"errors"
	"strings"
	"testing"
)

func TestUnraidShareKeyNamesOneFileThroughEveryMount(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"/mnt/user/domains/win/vdisk1.img":      "domains/win/vdisk1.img",
		"/mnt/user0/domains/win/vdisk1.img":     "domains/win/vdisk1.img",
		"/mnt/cache/domains/win/vdisk1.img":     "domains/win/vdisk1.img",
		"/mnt/disk3/domains/win/vdisk1.img":     "domains/win/vdisk1.img",
		"/mnt/nvme_pool/domains/win/vdisk1.img": "domains/win/vdisk1.img",
		"/mnt/user/domains/./win/../win/a.img":  "domains/win/a.img",
		"/mnt/remotes/nas/domains/a.img":        "",
		"/mnt/disks/ssd/domains/a.img":          "",
		"/mnt/addons/x/domains/a.img":           "",
		"/mnt/rootshare/user/domains/a.img":     "",
		"/mnt/user/domains":                     "",
		"/mnt/user":                             "",
		"/etc/libvirt/qemu/nvram/vars.fd":       "",
		"mnt/user/domains/a.img":                "",
	} {
		if got, _ := unraidShareKey(path); got != want {
			t.Errorf("unraidShareKey(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestVMTargetSetMatchesShareAliases(t *testing.T) {
	t.Parallel()
	targets := map[string]bool{}
	addVMTarget(targets, "/mnt/user/domains/win/vdisk1.img")
	addVMTarget(targets, "/etc/libvirt/qemu/nvram/vars.fd")
	for _, path := range []string{
		"/mnt/user/domains/win/vdisk1.img",
		"/mnt/cache/domains/win/vdisk1.img",
		"/mnt/disk1/domains/win/vdisk1.img",
		"/mnt/user0/domains/win//vdisk1.img",
		"/etc/libvirt/qemu/nvram/vars.fd",
	} {
		if !vmTargetInSet(targets, path) {
			t.Errorf("%s is not detected as a reviewed target", path)
		}
	}
	for _, path := range []string{
		"/mnt/user/domains/win/vdisk2.img",
		"/mnt/remotes/domains/win/vdisk1.img",
		"/mnt/disks/domains/win/vdisk1.img",
		"/etc/libvirt/qemu/nvram/other.fd",
	} {
		if vmTargetInSet(targets, path) {
			t.Errorf("%s is wrongly detected as a reviewed target", path)
		}
	}
}

func TestBuildApprovedVMPlanRejectsDisksAliasedThroughUnraidMounts(t *testing.T) {
	t.Parallel()
	second := strings.ReplaceAll(strings.ReplaceAll(vmcovDisk, "vda", "vdb"), "/mnt/user/", "/mnt/cache/")
	meta := `{"backup_type":"full","state":"shutoff","disks":[{"target":"vda","backup_file":"vdisk0.img","format":"raw"},{"target":"vdb","backup_file":"vdisk1.img","format":"raw"}]}`
	_, err := buildApprovedVMPlan("fixture", "", vmcovDomain("fixture", vmcovUUID, "", vmcovDisk+second), []byte(meta), []string{"vdisk0.img", "vdisk1.img"})
	if !errors.Is(err, ErrVMRestoreTargetCollision) {
		t.Fatalf("aliased disks: %v, want %v", err, ErrVMRestoreTargetCollision)
	}
}

func TestVMTargetSetAliasesOnlyThroughUserShareViews(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		stored, probe string
		alias         bool
	}{
		{"/mnt/fast/domains/g/disk.img", "/mnt/slow/domains/g/disk.img", false},
		{"/mnt/cache/domains/g/disk.img", "/mnt/disk1/domains/g/disk.img", false},
		{"/mnt/disk1/domains/g/disk.img", "/mnt/disk2/domains/g/disk.img", false},
		{"/mnt/user/domains/g/disk.img", "/mnt/fast/domains/g/disk.img", true},
		{"/mnt/fast/domains/g/disk.img", "/mnt/user/domains/g/disk.img", true},
		{"/mnt/user0/domains/g/disk.img", "/mnt/disk2/domains/g/disk.img", true},
		{"/mnt/disk2/domains/g/disk.img", "/mnt/user0/domains/g/disk.img", true},
		{"/mnt/user/domains/g/disk.img", "/mnt/user0/domains/g/disk.img", true},
		{"/mnt/user0/domains/g/disk.img", "/mnt/user/domains/g/disk.img", true},
		{"/mnt/fast/domains/g/disk.img", "/mnt/fast/domains/g/disk.img", true},
	} {
		targets := map[string]bool{}
		addVMTarget(targets, tc.stored)
		if got := vmTargetInSet(targets, tc.probe); got != tc.alias {
			t.Errorf("stored %s, probe %s: alias = %v, want %v", tc.stored, tc.probe, got, tc.alias)
		}
	}
}

func TestBuildApprovedVMPlanAcceptsSameRelativePathOnTwoPools(t *testing.T) {
	t.Parallel()
	first := strings.ReplaceAll(vmcovDisk, "/mnt/user/", "/mnt/fast/")
	second := strings.ReplaceAll(strings.ReplaceAll(vmcovDisk, "vda", "vdb"), "/mnt/user/", "/mnt/slow/")
	meta := `{"backup_type":"full","state":"shutoff","disks":[{"target":"vda","backup_file":"vdisk0.img","format":"raw"},{"target":"vdb","backup_file":"vdisk1.img","format":"raw"}]}`
	_, err := buildApprovedVMPlan("fixture", "", vmcovDomain("fixture", vmcovUUID, "", first+second), []byte(meta), []string{"vdisk0.img", "vdisk1.img"})
	if err != nil {
		t.Fatalf("disks on separate pools: %v", err)
	}
}

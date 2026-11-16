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
		if got := unraidShareKey(path); got != want {
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

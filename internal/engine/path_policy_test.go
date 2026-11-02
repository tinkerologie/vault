package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/tinkerologie/vault/internal/dedup"
)

func vmDomainXML(disk, nvram string) []byte {
	return fmt.Appendf(nil, `<domain><name>w</name><os><nvram>%s</nvram></os><devices><disk type="file" device="disk"><source file=%q/><target dev="vda"/></disk></devices></domain>`, nvram, disk)
}

func TestVMRestorePlanPathPolicy(t *testing.T) {
	const okDisk, okNVRAM = "/mnt/user/domains/w/vdisk1.img", "/etc/libvirt/qemu/nvram/u_VARS-pure-efi.fd"
	tests := []struct {
		name, disk, nvram, dest string
		wantErr                 bool
	}{
		{name: "original location", disk: okDisk, nvram: okNVRAM},
		{name: "cache pool", disk: "/mnt/cache/domains/w/vdisk1.qcow2", nvram: okNVRAM},
		{name: "disk over a boot script", disk: "/boot/config/go", nvram: okNVRAM, wantErr: true},
		{name: "disk in cron", disk: "/etc/cron.d/x.img", nvram: okNVRAM, wantErr: true},
		{name: "nvram over passwd", disk: okDisk, nvram: "/etc/passwd", wantErr: true},
		{name: "custom destination rewrites a hostile path", disk: "/etc/cron.d/x.img", nvram: "/etc/passwd", dest: filepath.Join(t.TempDir(), "r")},
		{name: "custom destination in system tree", disk: okDisk, nvram: okNVRAM, dest: "/etc/restore", wantErr: true},
		{name: "traversal base name", disk: "/mnt/user/..", nvram: okNVRAM, dest: filepath.Join(t.TempDir(), "r"), wantErr: true},
		{name: "traversal nvram base name", disk: okDisk, nvram: "/mnt/user/..", dest: filepath.Join(t.TempDir(), "r"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := buildVMRestorePlan(vmDomainXML(tt.disk, tt.nvram), tt.dest)
			if err == nil {
				err = normalizeVMRestorePlan(plan)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAuthorizeVolumeSources(t *testing.T) {
	t.Parallel()
	// Docker volume paths are covered in safepath: this run may not be root.
	legit := []string{"/mnt/user/appdata/plex", "/mnt/cache/appdata/db", "/boot/config/plugins/foo"}
	if err := authorizeVolumeSources(legit, nil); err != nil {
		t.Fatalf("legitimate sources refused: %v", err)
	}
	for _, hostile := range []string{"/etc/cron.d", "/boot/config", "/usr/local/sbin", "/root/.ssh", "/var/lib/libvirt"} {
		if err := authorizeVolumeSources([]string{hostile}, nil); err == nil {
			t.Errorf("%s authorized from backup metadata alone", hostile)
		}
		// A bind the container on this host already has is locally trusted.
		if err := authorizeVolumeSources([]string{hostile}, []string{hostile, "/mnt/x"}); err != nil {
			t.Errorf("%s refused though the live container has it: %v", hostile, err)
		}
	}
}

// Issue #543: a storage writer edits a backup's mount source to a system path.
func TestContainerRestoreRefusesBackupControlledSource(t *testing.T) {
	fx := newRestoreFixture458(t, true)
	for _, name := range []string{"config.json", "volumes.json"} {
		path := filepath.Join(fx.sourceDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		data = bytes.ReplaceAll(data, []byte(fx.volDir), []byte("/etc/vault-hostile-target"))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := newRestoreDaemon458()
	f.containerInspect = func(context.Context, string) (client.ContainerInspectResult, error) {
		return client.ContainerInspectResult{}, fmt.Errorf("no such container")
	}
	err := (&ContainerHandler{cli: f}).Restore(t.Context(), BackupItem{Name: "app", Settings: map[string]any{}}, fx.sourceDir, noopProgress)
	if err == nil || !strings.Contains(err.Error(), "refusing in-place restore") {
		t.Fatalf("err = %v, want an in-place refusal", err)
	}
}

func TestContainerRestoreRefusesSystemCustomDestination(t *testing.T) {
	fx := newRestoreFixture458(t, true)
	f, _ := newRestoreDaemon458()
	item := BackupItem{Name: "app", Settings: map[string]any{"restore_destination": "/etc/restored"}}
	err := (&ContainerHandler{cli: f}).Restore(t.Context(), item, fx.sourceDir, noopProgress)
	if err == nil || !strings.Contains(err.Error(), "approved locations") {
		t.Fatalf("err = %v", err)
	}
}

func TestLiveMountSources(t *testing.T) {
	var nilHandler *ContainerHandler
	if got := nilHandler.liveMountSources(t.Context(), "x"); got != nil {
		t.Fatalf("nil handler: %v", got)
	}
	f, _ := newRestoreDaemon458()
	var asked string
	f.containerInspect = func(_ context.Context, name string) (client.ContainerInspectResult, error) {
		asked = name
		var res client.ContainerInspectResult
		res.Container.Mounts = []container.MountPoint{{Source: "/opt/app/"}, {Source: ""}}
		return res, nil
	}
	got := (&ContainerHandler{cli: f}).liveMountSources(t.Context(), "/app")
	if asked != "app" || len(got) != 1 || got[0] != "/opt/app" {
		t.Fatalf("asked %q, got %v", asked, got)
	}
}

func TestChunkedVolumeSources(t *testing.T) {
	t.Parallel()
	var inspect restoreInspect
	inspect.Mounts = append(inspect.Mounts, inspect.Mounts...)
	m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		containerVolPrefix + "/config":  {Chunks: []dedup.ID{{1}}},
		containerVolPrefix + "/skipped": {Size: volumeSkippedSize},
	}}
	data := `{"Mounts":[
		{"Type":"bind","Source":"/mnt/user/appdata/a","Destination":"/config"},
		{"Type":"bind","Source":"/etc/localtime","Destination":"/skipped"},
		{"Type":"bind","Source":"/dev/dri","Destination":"/absent"},
		{"Type":"tmpfs","Source":"/x","Destination":"/config"}
	]}`
	inspect = inspectFromJSON(t, data)
	got := chunkedVolumeSources(m, inspect)
	if len(got) != 1 || got[0] != "/mnt/user/appdata/a" {
		t.Fatalf("sources = %v; skipped and absent mounts must not block a restore", got)
	}
}

func TestFolderBackupRefusesSymlinkToSystemTree(t *testing.T) {
	t.Parallel()
	link := filepath.Join(t.TempDir(), "share")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	h := &FolderHandler{}
	item := BackupItem{Name: "f", Settings: map[string]any{"path": link}}
	if _, err := h.Backup(t.Context(), item, t.TempDir(), noopProgress); err == nil || !strings.Contains(err.Error(), "refusing to back up") {
		t.Fatalf("Backup err = %v", err)
	}
	if _, err := h.BackupChunked(t.Context(), item, nil, nil, noopProgress); err == nil || !strings.Contains(err.Error(), "refusing to back up") {
		t.Fatalf("BackupChunked err = %v", err)
	}
}

func TestFolderRestoreDestinationPolicy(t *testing.T) {
	t.Parallel()
	h := &FolderHandler{}
	source := t.TempDir()
	// folder_meta.json is backup-controlled, so it never reaches /boot/config.
	if err := os.WriteFile(filepath.Join(source, "folder_meta.json"), []byte(`{"path":"/boot/config"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Restore(t.Context(), BackupItem{Name: "f", Settings: map[string]any{}}, source, noopProgress); err == nil || !strings.Contains(err.Error(), "approved locations") {
		t.Fatalf("metadata path err = %v", err)
	}
	// A local job's own path gets the folder policy, which does allow /boot.
	err := h.Restore(t.Context(), BackupItem{Name: "f", Settings: map[string]any{"path": "/boot"}}, source, noopProgress)
	if err == nil || !strings.Contains(err.Error(), "archive not found") {
		t.Fatalf("job flash path err = %v, want it to reach the archive lookup", err)
	}
	if err := h.Restore(t.Context(), BackupItem{Name: "f", Settings: map[string]any{"path": "/etc/ssh"}}, source, noopProgress); err == nil {
		t.Fatal("job path in a system tree allowed")
	}
	if err := h.RestoreChunked(t.Context(), BackupItem{Name: "f", Settings: map[string]any{"restore_destination": "/etc/x"}}, nil, dedup.ID{}, "/etc/x", noopProgress); err == nil {
		t.Fatal("chunked custom destination in a system tree allowed")
	}
	if err := h.RestoreChunked(t.Context(), BackupItem{Name: "f", Settings: map[string]any{}}, nil, dedup.ID{}, "/root/x", noopProgress); err == nil {
		t.Fatal("chunked job path in a system tree allowed")
	}
}

// Issue #545: metadata must not follow a parent swapped for a symlink out of
// the restore tree.
func TestApplyRestoredMetaStaysInBase(t *testing.T) {
	t.Parallel()
	base, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "swapped")); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o777)
	applyRestoredMeta(base, filepath.Join(base, "swapped", "victim"), restoredMeta{mode: &mode, uid: -1, gid: -1, modTime: time.Unix(1000, 0)})
	after, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("victim changed: %v %v", after.Mode(), after.ModTime())
	}
}

// A final-component symlink inside the tree is never followed for mode or mtime.
func TestApplyRestoredMetaDoesNotFollowFinalSymlink(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	victim := filepath.Join(base, "victim")
	if err := os.WriteFile(victim, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o777)
	applyRestoredMeta(base, link, restoredMeta{mode: &mode, uid: -1, gid: -1, modTime: time.Unix(1000, 0)})
	info, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 || info.ModTime().Equal(time.Unix(1000, 0)) {
		t.Fatalf("followed the link: %v %v", info.Mode(), info.ModTime())
	}
}

func TestApplyRestoredMetaAppliesToRealFile(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	file := filepath.Join(base, "sub", "f")
	writeFile458(t, file, "x")
	mode := os.FileMode(0o604)
	stamp := time.Unix(1000, 0)
	applyRestoredMeta(base, file, restoredMeta{mode: &mode, uid: -1, gid: -1, modTime: stamp})
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode || !info.ModTime().Equal(stamp) {
		t.Fatalf("metadata not applied: %v %v", info.Mode(), info.ModTime())
	}
	// Missing and suspicious paths are logged, never fatal.
	applyRestoredMeta(base, filepath.Join(base, "missing"), restoredMeta{mode: &mode, uid: -1, gid: -1})
	applyRestoredMeta(base, "relative", restoredMeta{mode: &mode, uid: -1, gid: -1})
}

func TestMkdirRestoredThroughDanglingSymlinkFails(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	if err := os.Symlink(filepath.Join(base, "gone"), filepath.Join(base, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := mkdirRestored(filepath.Join(base, "dangling"), 0o755); err == nil {
		t.Fatal("created through a dangling symlink")
	}
	if err := mkdirRestored(filepath.Join(base, "a", "b", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mkdirRestored(filepath.Join(base, "a", "b", "c"), 0o755); err != nil {
		t.Fatalf("existing directory: %v", err)
	}
}

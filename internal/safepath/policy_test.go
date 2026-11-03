package safepath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDestinationPolicies(t *testing.T) {
	t.Parallel()
	type check func(string) (string, error)
	cases := []struct {
		name  string
		check check
		allow []string
		deny  []string
	}{
		{
			"custom destination", CheckCustomRestoreDestination,
			[]string{"/mnt/user/restore", "/mnt/cache/x", "/tmp/r", "/boot/config/plugins/foo"},
			[]string{"/mnt", "/boot", "/boot/config", "/boot/config/go", "/boot/config/plugins", "/etc/cron.d", "/usr/local/emhttp", "/var/lib/x", "/opt/x", "/home/u", "/root", "relative/x", "/mnt/user/../../etc", "/mnt/a\nb"},
		},
		{
			"volume target", CheckVolumeTarget,
			[]string{"/mnt/user/appdata/plex", "/mnt/cache/appdata/x", "/mnt/disks/ssd/x", "/var/lib/docker/volumes/db/_data", "/var/lib/docker/volumes/db/_data/sub", "/boot/config/plugins/foo"},
			[]string{"/etc/cron.d", "/boot/config/go", "/boot/config", "/var/lib/docker/volumes/db", "/var/lib/docker/image", "/var/lib/foo", "/usr/local/sbin", "/root/.ssh"},
		},
		{
			"vm disk", CheckVMDiskTarget,
			[]string{"/mnt/user/domains/w/vdisk1.img", "/mnt/cache/domains/w/v.qcow2", "/var/lib/libvirt/images/v.qcow2"},
			[]string{"/etc/passwd", "/boot/config/go", "/usr/local/bin/x", "/var/lib/docker/x", "/root/x"},
		},
		{
			"vm nvram", CheckVMNVRAMTarget,
			[]string{"/etc/libvirt/qemu/nvram/u_VARS-pure-efi.fd", "/var/lib/libvirt/qemu/nvram/u.fd", "/mnt/user/domains/w/u.fd"},
			[]string{"/etc/passwd", "/etc/libvirt/qemu/w.xml", "/etc/libvirt/qemu/nvram/sub/u.fd", "/boot/config/go"},
		},
		{
			"folder location", CheckFolderLocation,
			[]string{"/mnt/user/share", "/boot", "/boot/config", "/tank/data", "/opt/data"},
			[]string{"/", "/etc", "/etc/ssh", "/root/.ssh", "/usr/local", "/var/lib/x", "/var", "/proc/1", "/private/etc/ssh"},
		},
		{
			"snapshot", CheckSnapshotLocation,
			[]string{"/mnt/cache/.vault/vault.db", "/boot/config/plugins/vault/vault.db", "/tmp/s/vault.db"},
			[]string{"/etc/passwd", "/usr/local/x.db", "/mnt", "/mnt/a\nSNAPSHOT_PATH=/etc/passwd"},
		},
	}
	for _, tc := range cases {
		for _, p := range tc.allow {
			// Docker's directories are unreadable to a non-root test run, and
			// an unresolvable path fails closed. The daemon runs as root.
			if _, err := tc.check(p); err != nil && !errors.Is(err, fs.ErrPermission) {
				t.Errorf("%s: %q refused: %v", tc.name, p, err)
			}
		}
		for _, p := range tc.deny {
			if _, err := tc.check(p); err == nil {
				t.Errorf("%s: %q allowed, want refusal", tc.name, p)
			}
		}
	}
}

func TestIsDockerVolumeData(t *testing.T) {
	t.Parallel()
	for p, want := range map[string]bool{
		"/var/lib/docker/volumes/db/_data":     true,
		"/var/lib/docker/volumes/db/_data/sub": true,
		"/var/lib/docker/volumes/db":           false,
		"/var/lib/docker/volumes/db/other":     false,
		"/var/lib/docker/image/_data":          false,
	} {
		if got := isDockerVolumeData(p); got != want {
			t.Errorf("isDockerVolumeData(%q) = %v", p, got)
		}
	}
}

// A symlink inside an allowed location must not carry a policy past its roots.
func TestPoliciesFollowSymlinks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]func(string) (string, error){
		"custom": CheckCustomRestoreDestination, "volume": CheckVolumeTarget, "disk": CheckVMDiskTarget,
		"nvram": CheckVMNVRAMTarget, "folder": CheckFolderLocation, "snapshot": CheckSnapshotLocation,
	} {
		if _, err := check(filepath.Join(link, "x")); err == nil {
			t.Errorf("%s: path through a symlink to /etc was allowed", name)
		}
	}
}

func TestCheckSnapshotLocationFileKinds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regular := filepath.Join(dir, "vault.db")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckSnapshotLocation(regular); err != nil {
		t.Errorf("regular file refused: %v", err)
	}
	if _, err := CheckSnapshotLocation(filepath.Join(dir, "backups..2026.db")); err == nil {
		t.Error("name containing consecutive dots allowed; its .tmp file could not be cleaned up")
	}
	// A parent directory that does not exist yet is fine: saves create it.
	if _, err := CheckSnapshotLocation(filepath.Join(dir, "new", "sub", "vault.db")); err != nil {
		t.Errorf("missing parent refused: %v", err)
	}
	if _, err := CheckSnapshotLocation(dir); err == nil {
		t.Error("directory allowed")
	}
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckSnapshotLocation(link); err == nil {
		t.Error("symlink to a file in the same root allowed")
	}
}

func TestRejectControlChars(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"a\nb", "a\rb", "a\x00b", "a\x7fb", "a\tb"} {
		if RejectControlChars(bad) == nil {
			t.Errorf("%q allowed", bad)
		}
	}
	if err := RejectControlChars("/mnt/user/café (1)"); err != nil {
		t.Errorf("ordinary path refused: %v", err)
	}
}

// Issue #554: /private/var/... is the same tree as /var/... on macOS.
func TestValidateLocalStorageRootPrivateAliases(t *testing.T) {
	t.Parallel()
	for _, p := range []string{
		"/private/var/log", "/private/var/log/vault", "/private/var/lib", "/private/var/lib/x",
		"/private/var/cache", "/private/var/cache/x", "/private/etc", "/private/etc/x",
	} {
		if _, err := ValidateLocalStorageRoot(p); err == nil {
			t.Errorf("%q allowed, want refusal", p)
		}
	}
	if got := canonicalAlias("/private/variety"); got != "/private/variety" {
		t.Errorf("canonicalAlias matched a partial component: %q", got)
	}
}

func TestOpenPinnedDir(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "a"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	root, err := OpenPinnedDir(real)
	if err != nil {
		t.Fatalf("OpenPinnedDir: %v", err)
	}
	// The handle stays on the original directory after the path is swapped.
	if err := os.Rename(real, real+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", real); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("pinned", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = root.Close()
	if _, err := os.Lstat(filepath.Join(real+".moved", "pinned")); err != nil {
		t.Errorf("write did not land in the pinned directory: %v", err)
	}

	if _, err := OpenPinnedDir(real); !errors.Is(err, ErrSymlinkInPath) {
		t.Errorf("swapped final component: err = %v", err)
	}
	if _, err := OpenPinnedDir(filepath.Join(dir, "link", "b")); !errors.Is(err, ErrSymlinkInPath) {
		t.Errorf("symlinked ancestor: err = %v", err)
	}
	if _, err := OpenPinnedDir(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: err = %v", err)
	}
	if _, err := OpenPinnedDir(filepath.Join(dir, "file")); err == nil {
		t.Error("regular file opened as a directory")
	}
	if _, err := OpenPinnedDir("relative"); err == nil {
		t.Error("relative path accepted")
	}
}

func TestNormalizeComponentRejectsDot(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{".", " . "} {
		if _, err := NormalizeComponent(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestResolvesUnderRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	roots := []string{"/mnt", "/boot"}
	if ResolvesUnderRoots(filepath.Join(link, "x"), roots) {
		t.Error("symlink to /etc accepted")
	}
	for _, ok := range []string{"/mnt", "/mnt/user/x", "/boot/config", filepath.Join(dir, "new")} {
		if !ResolvesUnderRoots(ok, roots) {
			t.Errorf("%q refused", ok)
		}
	}
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if ResolvesUnderRoots(filepath.Join(loop, "x"), roots) {
		t.Error("unresolvable path accepted")
	}
}

func TestOpenPinnedDirEdgeCases(t *testing.T) {
	t.Parallel()
	root, err := OpenPinnedDir(string(filepath.Separator))
	if err != nil {
		t.Fatalf("filesystem root: %v", err)
	}
	_ = root.Close()
	dir := t.TempDir()
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckCustomRestoreDestination(filepath.Join(loop, "x")); err == nil {
		t.Error("unresolvable destination accepted")
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(real, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if _, err := OpenPinnedDir(locked); err == nil {
		t.Error("opened an unreadable directory")
	}
}

func TestCheckStageCleanupLocation(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"/mnt/cache/.vault-stage", "/mnt/user/backups/.vault-stage", "/boot/.vault-stage", "/tmp/x/.vault-stage"} {
		if _, err := CheckStageCleanupLocation(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"/opt/app/.vault-stage", "/home/u/.vault-stage", "/etc/.vault-stage", "/mnt", "/tank/.vault-stage"} {
		if _, err := CheckStageCleanupLocation(bad); err == nil {
			t.Errorf("%q allowed", bad)
		}
	}
}

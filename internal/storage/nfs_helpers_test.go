package storage

import (
	"runtime"
	"testing"
)

// TestNewNFSAdapter_RejectsShellMetaChars locks in the injection guard
// inside NewNFSAdapter — the mount(8) command path runs arbitrary
// arguments, so any shell metacharacter in Host/Export/Version/Options
// must be rejected at construction.
func TestNewNFSAdapter_RejectsShellMetaChars(t *testing.T) {
	t.Parallel()
	bad := []NFSConfig{
		{Host: "h;rm -rf /", Export: "/exp"},
		{Host: "h", Export: "/exp|whoami"},
		{Host: "h", Export: "/exp", Version: "4`id`"},
		{Host: "h", Export: "/exp", Options: "ro,$(id)"},
		{Host: "h\n", Export: "/exp"},
	}
	for _, cfg := range bad {
		if _, err := NewNFSAdapter(cfg); err == nil {
			t.Errorf("expected rejection for %+v", cfg)
		}
	}
}

// TestNFSUnmount_NotMountedIsNoOp covers the early-return branch of
// unmount when the adapter was never mounted. The branch is guarded by
// `if !n.mounted`, so this exercises the no-op return without touching
// mount(8).
func TestNFSUnmount_NotMountedIsNoOp(t *testing.T) {
	t.Parallel()
	a, err := NewNFSAdapter(NFSConfig{Host: "h", Export: "/exp"})
	if err != nil {
		t.Fatal(err)
	}
	// adapter.mounted == false; should not panic, should not log error.
	a.unmount()

	// Close should also be safe when not mounted (delegates to unmount).
	if err := a.Close(); err != nil {
		t.Errorf("Close on unmounted adapter returned %v", err)
	}
}

// TestNFSClose_AfterFakeMountUnmounts exercises the mounted-branch of
// unmount: we white-box the adapter so it looks mounted against a
// host-owned temp dir, then verify Close() calls the unmount path
// (umount will fail with non-fatal log, mountDir cleanup proceeds).
//
// We avoid touching the real mount(8) command — that requires
// privileges and a live NFS server, neither of which is present on CI/
// macOS. The branch coverage we get here is the non-trivial half of
// the function (mounted=true → run umount → set local=nil → mounted=false).
func TestNFSClose_AfterFakeMountUnmounts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := &NFSAdapter{
		mountDir: dir,
		local:    NewLocalAdapter(dir),
		mounted:  true,
	}
	if err := a.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}
	if a.mounted {
		t.Error("mounted flag still true after Close")
	}
	if a.local != nil {
		t.Error("local adapter not cleared after Close")
	}
}

// TestNFSMount_BadHostReturnsError exercises the mount(8) failure
// branch. mount(8) on macOS may hang on unresponsive NFS endpoints,
// so we restrict the test to platforms where the command exits
// promptly (Linux). On other platforms we still get coverage from
// the other tests in this file plus the security-injection tests.
func TestNFSMount_BadHostReturnsError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("mount(8) behaviour outside Linux varies; skipping to keep -short fast")
	}
	t.Parallel()
	a, err := NewNFSAdapter(NFSConfig{
		Host:    "127.0.0.1",
		Export:  "/no/such/export",
		Version: "4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.mount(); err == nil {
		a.unmount()
		t.Skip("unexpected mount success; environment has live NFS at 127.0.0.1")
	}
}

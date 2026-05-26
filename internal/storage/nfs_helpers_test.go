package storage

import (
	"bytes"
	"io"
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

// newPreMountedNFSAdapter returns an NFSAdapter whose mounted=true bit
// is forced on and whose `local` field points at a host TempDir. The
// adapter's operations all call mount() first, which short-circuits
// when mounted is already true; this lets the delegating I/O paths run
// against a real filesystem without ever invoking mount(8).
func newPreMountedNFSAdapter(t *testing.T) (*NFSAdapter, string) {
	t.Helper()
	dir := t.TempDir()
	a := &NFSAdapter{
		mountDir: dir,
		local:    NewLocalAdapter(dir),
		mounted:  true,
	}
	return a, dir
}

// TestNFSDelegates_AllOperations exercises every delegating op so the
// mount-then-local-call pattern is hit on each.
func TestNFSDelegates_AllOperations(t *testing.T) {
	t.Parallel()
	a, _ := newPreMountedNFSAdapter(t)

	// Write
	if err := a.Write("a.bin", bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Read
	rc, err := a.Read("a.bin")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	body, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(body) != "hello" {
		t.Errorf("Read body = %q", body)
	}
	// Stat
	info, err := a.Stat("a.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size != 5 {
		t.Errorf("Stat size = %d, want 5", info.Size)
	}
	// List
	entries, err := a.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected at least one entry after Write")
	}
	// ReadRange
	rrc, err := a.ReadRange("a.bin", 1, 3)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	rng, _ := io.ReadAll(rrc)
	_ = rrc.Close()
	if string(rng) != "ell" {
		t.Errorf("ReadRange = %q, want %q", rng, "ell")
	}
	// Delete
	if err := a.Delete("a.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// TestConnection: this calls unmount() at the end, which would
	// try to umount(8) a temp dir — it'll log a warning but return
	// nil for the adapter (the umount error is non-fatal).
	a2, _ := newPreMountedNFSAdapter(t)
	if err := a2.TestConnection(); err != nil {
		t.Errorf("TestConnection: %v", err)
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

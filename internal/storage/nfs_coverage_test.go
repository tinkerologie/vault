package storage

import (
	"bytes"
	"context"
	"os"
	"testing"
)

// TestNFSMethods_MountFailure exercises the mount-failure error return of
// every wrapper method. We force MkdirTemp to fail (which is the very
// first line of mount()) by setting TMPDIR to a path that cannot be
// created under, so we never actually invoke the OS-level mount command.
// This is the half of every wrapper that TestNFSDelegates_AllOperations
// (which uses mounted=true to bypass mount entirely) cannot reach.
func TestNFSMethods_MountFailure(t *testing.T) {
	// Cannot run in parallel because we mutate TMPDIR for the whole
	// process. t.Setenv restores the original value automatically.
	t.Setenv("TMPDIR", "/nonexistent-path-for-nfs-mount-failure/should-not-exist")

	mk := func() *NFSAdapter {
		a, err := NewNFSAdapter(NFSConfig{
			Host:    "127.0.0.1",
			Export:  "/nonexistent-export-for-test",
			Version: "4",
		})
		if err != nil {
			t.Fatalf("NewNFSAdapter: %v", err)
		}
		return a
	}

	if err := mk().Write("x", bytes.NewReader([]byte("y"))); err == nil {
		t.Error("Write: expected mount failure")
	}
	if _, err := mk().Read("x"); err == nil {
		t.Error("Read: expected mount failure")
	}
	if _, err := mk().ReadRange("x", 0, 1); err == nil {
		t.Error("ReadRange: expected mount failure")
	}
	if err := mk().Delete("x"); err == nil {
		t.Error("Delete: expected mount failure")
	}
	if _, err := mk().List("x"); err == nil {
		t.Error("List: expected mount failure")
	}
	if _, err := mk().Stat("x"); err == nil {
		t.Error("Stat: expected mount failure")
	}
	if err := mk().TestConnection(); err == nil {
		t.Error("TestConnection: expected mount failure")
	}
	if _, err := mk().GetCapacity(context.Background()); err == nil {
		t.Error("GetCapacity: expected mount failure")
	}
}

// TestNFSMount_FailingMkdirTemp confirms that the very first error branch
// inside mount() (MkdirTemp failure) is reported up the stack as an error
// from mount(). This is structurally hit by TestNFSMethods_MountFailure
// already but is asserted directly here to lock in the error wrapping.
func TestNFSMount_FailingMkdirTemp(t *testing.T) {
	// Note: TMPDIR mutation; cannot parallelise.
	t.Setenv("TMPDIR", "/nonexistent-nfs-mount-dir-test-only")

	a, err := NewNFSAdapter(NFSConfig{Host: "h", Export: "/exp"})
	if err != nil {
		t.Fatalf("NewNFSAdapter: %v", err)
	}
	if err := a.mount(); err == nil {
		t.Fatal("expected mount() to fail when MkdirTemp can't create work dir")
	}

	// Ensure leftover mountDir state is empty.
	if a.mountDir != "" {
		t.Errorf("mountDir = %q, want empty after mount failure", a.mountDir)
	}
	// Confirm leftover temp dir is not present on the host (sanity).
	if a.mountDir != "" {
		if _, err := os.Stat(a.mountDir); err == nil {
			t.Errorf("temp mount dir %s still exists after failure", a.mountDir)
		}
	}
}

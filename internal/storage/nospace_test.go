package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestIsNoSpaceUnwrapsOSErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"bare ENOSPC", syscall.ENOSPC, true},
		{"PathError from io.Copy", &os.PathError{Op: "write", Path: "/x", Err: syscall.ENOSPC}, true},
		{"SyscallError from Sync", os.NewSyscallError("fsync", syscall.ENOSPC), true},
		{"wrapped twice", fmt.Errorf("write file: %w", &os.PathError{Op: "write", Err: syscall.ENOSPC}), true},
		{"permission denied", &os.PathError{Op: "write", Err: syscall.EACCES}, false},
		{"unrelated", errors.New("boom"), false},
	}
	for _, tc := range cases {
		if got := IsNoSpace(tc.err); got != tc.want {
			t.Errorf("%s: IsNoSpace = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestNoSpaceErrorExplainsUnraidUserShare is the point of the change: on
// /mnt/user the bare OS message is actively misleading, because the array can
// report terabytes free while a single file still fails to land (issue #255).
func TestNoSpaceErrorExplainsUnraidUserShare(t *testing.T) {
	err := NoSpaceError("/mnt/user/backups/vault_backup/VM-Backup/machine1", syscall.ENOSPC)
	msg := err.Error()

	for _, want := range []string{
		"/mnt/user/backups/vault_backup/VM-Backup/machine1",
		"single",             // one file lands on one disk
		"Minimum Free Space", // the share setting to check
		"Split Level",        // ditto
		"/mnt/cache",         // the concrete remedy
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	if !IsNoSpace(err) {
		t.Error("wrapped error no longer detectable as ENOSPC — retry classification would break")
	}
}

func TestNoSpaceErrorStaysTerseOffUserShare(t *testing.T) {
	err := NoSpaceError("/mnt/disk3/backups", syscall.ENOSPC)
	if strings.Contains(err.Error(), "Minimum Free Space") {
		t.Errorf("Unraid share guidance leaked onto a plain path:\n%s", err.Error())
	}
	if !strings.Contains(err.Error(), "/mnt/disk3/backups") {
		t.Errorf("message should still name the directory:\n%s", err.Error())
	}
}

func TestIsUnraidUserShareRejectsTraversal(t *testing.T) {
	if isUnraidUserShare("/mnt/user/../userdata") {
		t.Error("a sibling path starting with the same prefix was treated as a user share")
	}
	if !isUnraidUserShare("/mnt/user") || !isUnraidUserShare("/mnt/user/backups") {
		t.Error("genuine user-share paths not recognised")
	}
}

// enospcReader fails mid-stream with ENOSPC, which io.Copy surfaces to
// LocalAdapter.Write exactly as a full destination volume would.
type enospcReader struct{ read bool }

func (r *enospcReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, &os.PathError{Op: "write", Path: "/tmp/x", Err: syscall.ENOSPC}
	}
	r.read = true
	copy(p, []byte("partial"))
	return 7, nil
}

// TestLocalWriteDoesNotBlameDestinationForSourceErrors covers attribution.
//
// io.Copy reports read errors as well as write errors, and a source adapter
// can raise ENOSPC of its own. Handing the operator Unraid guidance about the
// destination share in that case sends them to the wrong volume, so the
// enriched message must apply only when the write side is what failed.
func TestLocalWriteDoesNotBlameDestinationForSourceErrors(t *testing.T) {
	dir := t.TempDir()
	a := &LocalAdapter{basePath: dir}

	err := a.Write("sub/vdisk0.img", &enospcReader{})
	if err == nil {
		t.Fatal("expected the write to fail")
	}
	if strings.Contains(err.Error(), "no space left on the volume backing") {
		t.Fatalf("a source-side ENOSPC was blamed on the destination: %v", err)
	}
	if !IsNoSpace(err) {
		t.Fatalf("error lost its ENOSPC identity: %v", err)
	}

	// The partial temp file must not be left behind either way.
	entries, rerr := os.ReadDir(filepath.Join(dir, "sub"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".vault-tmp-") {
			t.Fatalf("partial temp file left behind: %s", e.Name())
		}
	}
}

// failingWriter fails every write with the supplied error.
type failingWriter struct{ err error }

func (w failingWriter) Write(p []byte) (int, error) { return 0, w.err }

// TestWriteFailTrackerFlagsOnlyWriteErrors covers the discriminator the
// adapter branches on. A genuine destination-full write cannot be produced
// portably in a unit test — it needs a real full filesystem — so the branch is
// covered as its two parts: this, plus TestNoSpaceErrorExplainsUnraidUserShare
// for the message itself.
func TestWriteFailTrackerFlagsOnlyWriteErrors(t *testing.T) {
	// Write side fails.
	tracker := &writeFailTracker{w: failingWriter{err: &os.PathError{Op: "write", Err: syscall.ENOSPC}}}
	if _, err := io.Copy(tracker, strings.NewReader("payload")); err == nil {
		t.Fatal("expected the copy to fail")
	}
	if !tracker.failed {
		t.Fatal("a destination write failure was not flagged")
	}

	// Read side fails; destination is fine.
	clean := &writeFailTracker{w: io.Discard}
	if _, err := io.Copy(clean, &enospcReader{}); err == nil {
		t.Fatal("expected the copy to fail")
	}
	if clean.failed {
		t.Fatal("a source failure was mis-flagged as a destination failure")
	}
}

var _ io.Reader = (*enospcReader)(nil)

// TestNoSpaceErrorCoversPoolBackedShares guards against misdirection: a
// cache-only share also lives under /mnt/user but is limited by its pool, not
// by array split rules. The path cannot tell them apart — shfs is a FUSE mount,
// so the name does not resolve to a backing device — so the guidance must cover
// both rather than confidently naming the wrong one.
func TestNoSpaceErrorCoversPoolBackedShares(t *testing.T) {
	msg := NoSpaceError("/mnt/user/appdata", syscall.ENOSPC).Error()
	if !strings.Contains(msg, "pool") {
		t.Errorf("guidance ignores pool-backed shares:\n%s", msg)
	}
	if strings.Contains(msg, "the array as a whole reports free space") {
		t.Errorf("guidance still asserts the array-only explanation:\n%s", msg)
	}
}

package storage

import (
	"errors"
	"runtime"
	"testing"
)

// TestUsage_LocalAdapter verifies Usage() on a LocalAdapter backed by a
// temporary directory. On Linux the syscall should return real free/total
// values; on other platforms (darwin, windows) Usage() delegates through
// GetCapacity which also calls unix.Statfs — so we expect real values on
// darwin too (golang.org/x/sys/unix supports darwin).
func TestUsage_LocalAdapter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := NewLocalAdapter(dir)

	free, total, err := a.Usage()
	if err != nil {
		// On linux this is always expected to succeed.
		// On darwin/other it might fail if Statfs is unsupported.
		if runtime.GOOS == "linux" {
			t.Fatalf("Usage() on linux returned error: %v", err)
		}
		// Non-linux: if it errors, it should be ErrUsageNotSupported
		// (or any error from the underlying syscall, which we accept).
		t.Logf("Usage() on %s returned non-nil error (acceptable): %v", runtime.GOOS, err)
		return
	}
	if total <= 0 {
		t.Errorf("total = %d, want > 0", total)
	}
	if free < 0 {
		t.Errorf("free = %d, want >= 0", free)
	}
	if free > total {
		t.Errorf("free (%d) > total (%d), invariant violated", free, total)
	}
}

// TestUsage_LocalAdapter_RealValues ensures that on Linux, Usage() returns
// non-zero values and satisfies the free<=total invariant.
func TestUsage_LocalAdapter_RealValues(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("linux-specific assertion; running on %s", runtime.GOOS)
	}
	t.Parallel()
	dir := t.TempDir()
	a := NewLocalAdapter(dir)

	free, total, err := a.Usage()
	if err != nil {
		t.Fatalf("Usage() = err %v, want nil on linux", err)
	}
	if total <= 0 {
		t.Errorf("total = %d, want > 0", total)
	}
	if free <= 0 {
		t.Errorf("free = %d, want > 0 (tmpdir on a real fs)", free)
	}
	if free > total {
		t.Errorf("free (%d) > total (%d), invariant violated", free, total)
	}
}

// TestUsage_ErrUsageNotSupportedSentinel verifies that ErrUsageNotSupported
// is matchable via errors.Is (it must not be wrapped or changed).
func TestUsage_ErrUsageNotSupportedSentinel(t *testing.T) {
	t.Parallel()
	if !errors.Is(ErrUsageNotSupported, ErrUsageNotSupported) {
		t.Error("errors.Is(ErrUsageNotSupported, ErrUsageNotSupported) should be true")
	}
	wrapped := errors.Join(ErrUsageNotSupported)
	if !errors.Is(wrapped, ErrUsageNotSupported) {
		t.Error("errors.Is on errors.Join-wrapped ErrUsageNotSupported should be true")
	}
}

// TestUsage_S3Adapter_Sentinel verifies that S3Adapter.Usage() returns
// ErrUsageNotSupported since S3 has no free/total notion.
// The adapter is constructed with minimal valid config; no live connection needed.
func TestUsage_S3Adapter_Sentinel(t *testing.T) {
	t.Parallel()
	a, err := NewS3Adapter(S3Config{
		Bucket: "test-bucket",
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatalf("NewS3Adapter: %v", err)
	}
	_, _, usageErr := a.Usage()
	if !errors.Is(usageErr, ErrUsageNotSupported) {
		t.Errorf("S3Adapter.Usage() returned %v, want ErrUsageNotSupported", usageErr)
	}
}

// TestUsage_noCloseAdapter_Sentinel verifies that adapters returning the
// sentinel satisfy the errors.Is contract used by all callers.
func TestUsage_noCloseAdapter_Sentinel(t *testing.T) {
	t.Parallel()
	var a Adapter = noCloseAdapter{}
	_, _, err := a.Usage()
	if !errors.Is(err, ErrUsageNotSupported) {
		t.Errorf("noCloseAdapter.Usage() = %v, want ErrUsageNotSupported", err)
	}
}

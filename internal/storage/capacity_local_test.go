package storage

import (
	"bytes"
	"context"
	"runtime"
	"testing"
)

// TestGetCapacity_LocalAdapter verifies GetCapacity on a LocalAdapter backed
// by a temporary directory. On Linux the syscall always returns real values;
// on darwin golang.org/x/sys/unix supports Statfs too, so real values are
// expected there as well. Other platforms may legitimately fail.
func TestGetCapacity_LocalAdapter(t *testing.T) {
	t.Parallel()
	a := NewLocalAdapter(t.TempDir())

	cap, err := a.GetCapacity(context.Background())
	if err != nil {
		if runtime.GOOS == "linux" {
			t.Fatalf("GetCapacity() on linux returned error: %v", err)
		}
		t.Logf("GetCapacity() on %s returned non-nil error (acceptable): %v", runtime.GOOS, err)
		return
	}
	if cap.TotalBytes <= 0 {
		t.Errorf("TotalBytes = %d, want > 0", cap.TotalBytes)
	}
	if cap.FreeBytes < 0 {
		t.Errorf("FreeBytes = %d, want >= 0", cap.FreeBytes)
	}
	if cap.FreeBytes > cap.TotalBytes {
		t.Errorf("FreeBytes (%d) > TotalBytes (%d), invariant violated", cap.FreeBytes, cap.TotalBytes)
	}
}

// TestGetCapacity_LocalAdapter_RealValues ensures that on Linux a tmpdir on a
// real filesystem reports non-zero free space.
func TestGetCapacity_LocalAdapter_RealValues(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("linux-specific assertion; running on %s", runtime.GOOS)
	}
	t.Parallel()
	a := NewLocalAdapter(t.TempDir())

	cap, err := a.GetCapacity(context.Background())
	if err != nil {
		t.Fatalf("GetCapacity() = err %v, want nil on linux", err)
	}
	if cap.TotalBytes <= 0 {
		t.Errorf("TotalBytes = %d, want > 0", cap.TotalBytes)
	}
	if cap.FreeBytes <= 0 {
		t.Errorf("FreeBytes = %d, want > 0 (tmpdir on a real fs)", cap.FreeBytes)
	}
}

// TestGetCapacity_S3Adapter_NoQuota verifies that S3 reports "no quota
// available" as TotalBytes == 0 while still summing what is stored. This is
// the signal the capacity sampler uses to skip a destination, so it is
// load-bearing: S3 has no per-bucket quota API, and UsedBytes alone must not
// produce a trajectory sample.
//
// It runs against the in-memory S3 fake rather than a cancelled context: a
// cancelled probe returns a zero Capacity whatever the adapter believes, so
// asserting on it would pass even if S3 started reporting a quota.
func TestGetCapacity_S3Adapter_NoQuota(t *testing.T) {
	t.Parallel()
	a, _, closeFn := newS3MockAdapter(t)
	defer closeFn()

	ctx := context.Background()
	if err := a.Write("capacity/probe.bin", bytes.NewReader(make([]byte, 2048))); err != nil {
		t.Fatalf("seeding object: %v", err)
	}

	capacity, err := a.GetCapacity(ctx)
	if err != nil {
		t.Fatalf("GetCapacity: %v", err)
	}
	if capacity.TotalBytes != 0 {
		t.Errorf("S3 TotalBytes = %d, want 0 (no per-bucket quota API)", capacity.TotalBytes)
	}
	if capacity.UsedBytes != 2048 {
		t.Errorf("S3 UsedBytes = %d, want 2048 (sum of stored objects)", capacity.UsedBytes)
	}
	// A zero total with a non-zero used must not become a trajectory sample.
	if _, _, ok := capacitySampleable(capacity); ok {
		t.Error("a quota-less destination produced a capacity sample")
	}
}

// capacitySampleable mirrors the runner's sampling rule: no total, no sample.
func capacitySampleable(c Capacity) (free, total int64, ok bool) {
	if c.TotalBytes <= 0 {
		return 0, 0, false
	}
	return c.FreeBytes, c.TotalBytes, true
}

package storage

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// TestAutoThrottleCountsUploadBytes verifies the adaptive wrapper counts
// bytes pushed through upload readers (the control loop's Vault-traffic
// estimate) and that disabling the limit makes the wrapper a pass-through.
func TestAutoThrottleCountsUploadBytes(t *testing.T) {
	SetAutoThrottleLimit(0) // ensure disabled: reads must not block
	before := AutoThrottleUploadedBytes()

	payload := bytes.Repeat([]byte("x"), 64*1024)
	tr := &throttledReader{r: bytes.NewReader(payload), dynamic: true, countUpload: true}
	n, err := io.Copy(io.Discard, tr)
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("copy = %d, %v; want %d, nil", n, err, len(payload))
	}
	if got := AutoThrottleUploadedBytes() - before; got != int64(len(payload)) {
		t.Fatalf("counted %d uploaded bytes, want %d", got, len(payload))
	}

	// Download-style reader (countUpload=false) must not count.
	before = AutoThrottleUploadedBytes()
	trc := &throttledReadCloser{rc: io.NopCloser(bytes.NewReader(payload)), dynamic: true}
	if _, err := io.Copy(io.Discard, trc); err != nil {
		t.Fatalf("download copy: %v", err)
	}
	if got := AutoThrottleUploadedBytes() - before; got != 0 {
		t.Fatalf("download counted %d bytes toward uploads, want 0", got)
	}
}

// TestSetAutoThrottleLimitRetunes exercises install → retune → disable.
func TestSetAutoThrottleLimitRetunes(t *testing.T) {
	SetAutoThrottleLimit(1_000_000)
	if lim := currentAutoLimiter(); lim == nil || float64(lim.Limit()) != 1_000_000 {
		t.Fatalf("limiter not installed at 1MB/s: %+v", lim)
	}
	SetAutoThrottleLimit(2_000_000)
	if lim := currentAutoLimiter(); lim == nil || float64(lim.Limit()) != 2_000_000 {
		t.Fatalf("limiter not adjusted to 2MB/s: %+v", lim)
	}
	SetAutoThrottleLimit(0)
	if lim := currentAutoLimiter(); lim != nil {
		t.Fatal("limiter should be nil when disabled")
	}
}

// TestLocalDestinationStaticThrottle verifies the per-destination bandwidth
// cap now applies to local destinations too (issue #237 follow-up): capping
// the disk write rate is how local backups leave I/O headroom for streaming
// apps reading from the same array.
func TestLocalDestinationStaticThrottle(t *testing.T) {
	dir := t.TempDir()
	payload := bytes.Repeat([]byte("x"), 2500*1024) // ~2.44 MiB

	// 8 Mbps = 1 MB/s with a 1-second burst: ~1 MB goes through instantly,
	// the remaining ~1.4 MB must take over a second. Assert >500ms to stay
	// far from scheduler flakiness.
	limited, err := NewAdapter("local", `{"path":"`+dir+`","bandwidth_limit_mbps":8}`)
	if err != nil {
		t.Fatalf("NewAdapter limited: %v", err)
	}
	defer CloseAdapter(limited)
	start := time.Now()
	if err := limited.Write("capped.bin", bytes.NewReader(payload)); err != nil {
		t.Fatalf("throttled write: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Fatalf("throttled 2.4MiB write at 1MB/s finished in %v — cap not applied", elapsed)
	}

	unlimited, err := NewAdapter("local", `{"path":"`+dir+`"}`)
	if err != nil {
		t.Fatalf("NewAdapter unlimited: %v", err)
	}
	defer CloseAdapter(unlimited)
	start = time.Now()
	if err := unlimited.Write("free.bin", bytes.NewReader(payload)); err != nil {
		t.Fatalf("unlimited write: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("unlimited write took %v — unexpected throttling", elapsed)
	}
}

// TestLocalThrottleIsWriteOnly verifies restores/verification reads from a
// capped local destination run at full speed — the disk write limit must
// never slow an urgent restore.
func TestLocalThrottleIsWriteOnly(t *testing.T) {
	dir := t.TempDir()
	limited, err := NewAdapter("local", `{"path":"`+dir+`","bandwidth_limit_mbps":8}`)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	defer CloseAdapter(limited)

	// Seed a ~2.44 MiB file (the write itself is capped; that's fine here).
	payload := bytes.Repeat([]byte("y"), 2500*1024)
	if err := limited.Write("readback.bin", bytes.NewReader(payload)); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	start := time.Now()
	rc, err := limited.Read("readback.bin")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	n, err := io.Copy(io.Discard, rc)
	_ = rc.Close()
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("read copy = %d, %v", n, err)
	}
	// At the 1 MB/s write cap this read would take >1s if throttled; a
	// pass-through read of 2.4 MiB from tmpfs/SSD is effectively instant.
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("read of capped local destination took %v — reads must not be throttled", elapsed)
	}
}

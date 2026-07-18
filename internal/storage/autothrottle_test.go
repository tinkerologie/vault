package storage

import (
	"bytes"
	"io"
	"testing"
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

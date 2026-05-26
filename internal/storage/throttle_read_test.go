package storage

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestThrottledAdapter_ReadDelegatesAndReturnsBytes confirms that the
// throttled adapter's Read method wraps the inner adapter's body and
// passes the bytes through unmodified.
func TestThrottledAdapter_ReadDelegatesAndReturnsBytes(t *testing.T) {
	t.Parallel()
	inner := newRecordingAdapter()
	inner.data["file.bin"] = []byte("hello world")

	throttled := WrapThrottled(inner, 100) // 100 Mbps, no perceivable delay on 11 bytes

	rc, err := throttled.Read("file.bin")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("Read returned %q, want %q", got, "hello world")
	}
}

// TestThrottledAdapter_ReadPropagatesInnerError ensures errors from the
// inner adapter surface to the caller before the wrapper drains any
// tokens.
func TestThrottledAdapter_ReadPropagatesInnerError(t *testing.T) {
	t.Parallel()
	inner := newRecordingAdapter() // empty -> Read returns io.ErrUnexpectedEOF
	throttled := WrapThrottled(inner, 10)

	rc, err := throttled.Read("missing")
	if err == nil {
		_ = rc.Close()
		t.Fatal("expected error from inner.Read")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("got err=%v, want io.ErrUnexpectedEOF", err)
	}
}

// TestThrottledAdapter_ReadRangeThreadsParams asserts that the offset
// and length arguments survive the wrapper round-trip and the returned
// bytes match what the inner adapter sliced.
func TestThrottledAdapter_ReadRangeThreadsParams(t *testing.T) {
	t.Parallel()
	inner := newRecordingAdapter()
	inner.data["range.bin"] = []byte("the quick brown fox")

	throttled := WrapThrottled(inner, 100)
	rc, err := throttled.ReadRange("range.bin", 4, 5)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "quick" {
		t.Errorf("ReadRange returned %q, want %q", got, "quick")
	}
}

// TestThrottledAdapter_ReadRangePropagatesError exercises the early-
// error path when the inner ReadRange call fails.
func TestThrottledAdapter_ReadRangePropagatesError(t *testing.T) {
	t.Parallel()
	inner := newRecordingAdapter()
	throttled := WrapThrottled(inner, 10)

	rc, err := throttled.ReadRange("nope", 0, 10)
	if err == nil {
		_ = rc.Close()
		t.Fatal("expected error from inner.ReadRange")
	}
}

// closeTracker is a minimal ReadCloser whose Close sets a flag; used to
// confirm throttledReadCloser.Close forwards to the wrapped body.
type closeTracker struct {
	io.Reader
	closed bool
}

func (c *closeTracker) Close() error {
	c.closed = true
	return nil
}

// closeTrackerAdapter wraps recordingAdapter but returns a closeTracker
// from Read so the test can observe the close call.
type closeTrackerAdapter struct {
	recordingAdapter
	tracker *closeTracker
}

func (c *closeTrackerAdapter) Read(p string) (io.ReadCloser, error) {
	b, ok := c.data[p]
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}
	c.tracker = &closeTracker{Reader: bytes.NewReader(b)}
	return c.tracker, nil
}

func (c *closeTrackerAdapter) ReadRange(p string, offset, length int64) (io.ReadCloser, error) {
	b, ok := c.data[p]
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}
	if offset > int64(len(b)) {
		offset = int64(len(b))
	}
	end := offset + length
	if end > int64(len(b)) {
		end = int64(len(b))
	}
	c.tracker = &closeTracker{Reader: bytes.NewReader(b[offset:end])}
	return c.tracker, nil
}

// TestThrottledReadCloser_CloseForwardsToInner asserts that closing the
// wrapper closes the underlying body. Critical for keeping HTTP response
// bodies / file handles from leaking on cancellation paths.
func TestThrottledReadCloser_CloseForwardsToInner(t *testing.T) {
	t.Parallel()
	inner := &closeTrackerAdapter{recordingAdapter: *newRecordingAdapter()}
	inner.data["a"] = []byte("x")
	throttled := WrapThrottled(inner, 100)

	rc, err := throttled.Read("a")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !inner.tracker.closed {
		t.Error("inner body Close not invoked")
	}
}

// TestThrottledReadCloser_CloseForwardsViaReadRange covers the same
// closer-forwarding contract on the ReadRange path.
func TestThrottledReadCloser_CloseForwardsViaReadRange(t *testing.T) {
	t.Parallel()
	inner := &closeTrackerAdapter{recordingAdapter: *newRecordingAdapter()}
	inner.data["a"] = []byte("xyzzy")
	throttled := WrapThrottled(inner, 100)

	rc, err := throttled.ReadRange("a", 0, 3)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !inner.tracker.closed {
		t.Error("inner ReadRange body Close not invoked")
	}
}

// TestThrottledReader_RateLimiterChargesTokensOnRead drives the
// throttledReader (Write path) at an extremely small cap to confirm the
// limiter actually waits. We use a 1 KB/s cap and a 4 KiB payload — the
// burst-1s capacity is also 1 KB, so the second-onward bytes must wait
// against the limiter. With 4 KiB we expect ~3 s of waiting (4 KB at
// 1 KB/s, minus the 1 KB burst).
func TestThrottledReader_RateLimiterChargesTokensOnRead(t *testing.T) {
	// Not Parallel: this test asserts on wall time.
	bytesPerSec := 1024.0 // 1 KiB/s
	limiter := rate.NewLimiter(rate.Limit(bytesPerSec), int(bytesPerSec))
	src := bytes.NewReader(bytes.Repeat([]byte{0xAB}, 4*1024)) // 4 KiB
	tr := &throttledReader{r: src, limiter: limiter}

	start := time.Now()
	got, err := io.ReadAll(tr)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 4*1024 {
		t.Errorf("read %d bytes, want %d", len(got), 4*1024)
	}
	// 4 KiB at 1 KiB/s = 4 s nominal, minus 1 s burst = 3 s.
	// Use a generous lower bound to keep CI happy.
	if elapsed < 1500*time.Millisecond {
		t.Errorf("throttled read completed in %s, expected >=1.5s (rate=1 KB/s, 4 KiB payload)", elapsed)
	}
}

// TestThrottledReadCloser_RateLimiterChargesTokens does the same on the
// Read/ReadRange path (throttledReadCloser). Same maths, same window.
func TestThrottledReadCloser_RateLimiterChargesTokens(t *testing.T) {
	// Not Parallel: wall-time assertion.
	bytesPerSec := 1024.0
	limiter := rate.NewLimiter(rate.Limit(bytesPerSec), int(bytesPerSec))
	src := io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{0xCD}, 4*1024)))
	trc := &throttledReadCloser{rc: src, limiter: limiter}

	start := time.Now()
	got, err := io.ReadAll(trc)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := trc.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if len(got) != 4*1024 {
		t.Errorf("read %d bytes, want %d", len(got), 4*1024)
	}
	if elapsed < 1500*time.Millisecond {
		t.Errorf("throttled read closer completed in %s, expected >=1.5s", elapsed)
	}
}

// TestThrottledReadCloser_EmptyReadDoesNotWait asserts the n>0 branch
// guards against waiting for zero-byte reads (paranoia; would deadlock
// on an empty body without it).
func TestThrottledReadCloser_EmptyReadDoesNotWait(t *testing.T) {
	t.Parallel()
	bytesPerSec := 1.0 // crushingly slow
	limiter := rate.NewLimiter(rate.Limit(bytesPerSec), 1)
	src := io.NopCloser(bytes.NewReader(nil))
	trc := &throttledReadCloser{rc: src, limiter: limiter}

	start := time.Now()
	buf := make([]byte, 32)
	n, err := trc.Read(buf)
	elapsed := time.Since(start)
	if n != 0 {
		t.Errorf("read %d bytes from empty src, want 0", n)
	}
	if err != io.EOF {
		t.Errorf("err = %v, want io.EOF", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("empty read waited %s — limiter should not be charged on n=0", elapsed)
	}
	_ = trc.Close()
}

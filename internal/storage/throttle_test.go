package storage

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// recordingAdapter is a minimal in-memory Adapter for tests. It accepts
// writes into a map and replays them on Read.
type recordingAdapter struct {
	data map[string][]byte
}

func newRecordingAdapter() *recordingAdapter {
	return &recordingAdapter{data: map[string][]byte{}}
}

func (r *recordingAdapter) Write(p string, body io.Reader) error {
	buf, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	r.data[p] = buf
	return nil
}
func (r *recordingAdapter) Read(p string) (io.ReadCloser, error) {
	b, ok := r.data[p]
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (r *recordingAdapter) ReadRange(p string, offset, length int64) (io.ReadCloser, error) {
	b, ok := r.data[p]
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}
	if offset >= int64(len(b)) {
		return nil, io.EOF
	}
	end := offset + length
	if end > int64(len(b)) {
		end = int64(len(b))
	}
	return io.NopCloser(bytes.NewReader(b[offset:end])), nil
}
func (r *recordingAdapter) Delete(p string) error                  { delete(r.data, p); return nil }
func (r *recordingAdapter) List(prefix string) ([]FileInfo, error) { return nil, nil }
func (r *recordingAdapter) Stat(p string) (FileInfo, error)        { return FileInfo{}, nil }
func (r *recordingAdapter) TestConnection() error                  { return nil }

func TestWrapThrottled_ZeroPassThrough(t *testing.T) {
	inner := newRecordingAdapter()
	got := WrapThrottled(inner, 0)
	if got != inner {
		t.Errorf("mbps=0 should return the same adapter, got wrapper")
	}
}

func TestWrapThrottled_NegativePassThrough(t *testing.T) {
	inner := newRecordingAdapter()
	got := WrapThrottled(inner, -5)
	if got != inner {
		t.Errorf("negative mbps should pass through, got wrapper")
	}
}

func TestWrapThrottled_NilAdapter(t *testing.T) {
	if got := WrapThrottled(nil, 10); got != nil {
		t.Errorf("nil inner must stay nil, got %T", got)
	}
}

// TestThrottled_Enforces16MbpsCap writes roughly 4 MB through a 16 Mbps cap
// (= 2 MB/s) and asserts wall time is at least ~1.5 s. We don't check an
// upper bound because CI runners vary; the lower bound is the cap working.
func TestThrottled_Enforces16MbpsCap(t *testing.T) {
	const sizeBytes = 4 * 1024 * 1024 // 4 MiB
	const mbps = 16                   // 2 MiB/s
	inner := newRecordingAdapter()
	throttled := WrapThrottled(inner, mbps)
	payload := strings.NewReader(strings.Repeat("x", sizeBytes))

	start := time.Now()
	if err := throttled.Write("k", payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	elapsed := time.Since(start)
	// 4 MiB at 2 MiB/s = ~2 s nominal. Burst capacity drains the first
	// 1 s of bytes without waiting, so we expect ~1 s real wait. Use
	// 0.8 s lower bound for CI slack.
	if elapsed < 800*time.Millisecond {
		t.Errorf("throttled write completed too fast (%s) for cap=%d Mbps over %d bytes", elapsed, mbps, sizeBytes)
	}
}

func TestThrottled_MetadataNotThrottled(t *testing.T) {
	// List/Stat/Delete/TestConnection should never block on the limiter.
	// We can't directly assert "no token consumed" but we can confirm
	// they complete in essentially zero time even with a tiny cap.
	inner := newRecordingAdapter()
	throttled := WrapThrottled(inner, 1) // 1 Mbps = 125 KB/s

	start := time.Now()
	for i := 0; i < 100; i++ {
		_, _ = throttled.List("/")
		_, _ = throttled.Stat("/")
		_ = throttled.Delete("/missing")
		_ = throttled.TestConnection()
	}
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Errorf("metadata ops should not be throttled; took %s for 400 calls", elapsed)
	}
}

package storage

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

// throttledAdapter wraps another Adapter and limits the bytes-per-second of
// every Read() body and Write() body to a shared token bucket. List, Stat,
// Delete, and TestConnection are pass-through — they're metadata operations
// and shouldn't count toward the bandwidth cap.
//
// The limit is symmetric (one bucket shared between upload and download)
// because users typically want a single "don't saturate my uplink" knob and
// most home backups push far more bytes than they pull. A per-direction cap
// would be useful for restic-style mirrored repositories but not for Vault's
// current backup/restore workflow.
type throttledAdapter struct {
	inner   Adapter
	limiter *rate.Limiter
}

// WrapThrottled returns adapter unchanged when mbps <= 0; otherwise wraps it
// in a throttled adapter capped at the requested rate in megabits per second.
//
// We use Mbits (not MiB) because that's the unit ISPs quote uplinks in, so
// "5" maps directly to "5 Mbps of my 50 Mbps connection". One token = one
// byte; the bucket holds 1 second of bytes (capacity = burst) so a slow read
// loop never starves on token underflow.
func WrapThrottled(adapter Adapter, mbps int) Adapter {
	if mbps <= 0 || adapter == nil {
		return adapter
	}
	bytesPerSec := float64(mbps) * 1_000_000 / 8
	limiter := rate.NewLimiter(rate.Limit(bytesPerSec), int(bytesPerSec))
	return &throttledAdapter{inner: adapter, limiter: limiter}
}

func (t *throttledAdapter) Write(p string, reader io.Reader) error {
	return t.inner.Write(p, &throttledReader{r: reader, limiter: t.limiter})
}

func (t *throttledAdapter) Read(p string) (io.ReadCloser, error) {
	rc, err := t.inner.Read(p)
	if err != nil {
		return nil, err
	}
	return &throttledReadCloser{rc: rc, limiter: t.limiter}, nil
}

func (t *throttledAdapter) ReadRange(p string, offset, length int64) (io.ReadCloser, error) {
	rc, err := t.inner.ReadRange(p, offset, length)
	if err != nil {
		return nil, err
	}
	return &throttledReadCloser{rc: rc, limiter: t.limiter}, nil
}

func (t *throttledAdapter) Delete(p string) error { return t.inner.Delete(p) }
func (t *throttledAdapter) List(prefix string) ([]FileInfo, error) {
	return t.inner.List(prefix)
}
func (t *throttledAdapter) Stat(p string) (FileInfo, error) { return t.inner.Stat(p) }
func (t *throttledAdapter) TestConnection() error           { return t.inner.TestConnection() }

// throttledReader wraps an io.Reader and waits at the limiter before
// returning each chunk. The wait is sized to the actual bytes read so a
// reader that returns 4 KiB on a 5 Mbps cap (= ~625 KB/s) waits ~6.4 ms
// per read \xe2\x80\x94 imperceptible to the caller but enforces the cap.
type throttledReader struct {
	r       io.Reader
	limiter *rate.Limiter
}

func (tr *throttledReader) Read(p []byte) (int, error) {
	n, err := tr.r.Read(p)
	if n > 0 {
		// WaitN blocks until enough tokens accumulate. Burst capacity
		// equals one second of bytes so the very first read after a
		// quiet period drains the bucket without blocking; sustained
		// throughput is then capped at the configured rate.
		_ = tr.limiter.WaitN(context.Background(), n)
	}
	return n, err
}

type throttledReadCloser struct {
	rc      io.ReadCloser
	limiter *rate.Limiter
}

func (trc *throttledReadCloser) Read(p []byte) (int, error) {
	n, err := trc.rc.Read(p)
	if n > 0 {
		_ = trc.limiter.WaitN(context.Background(), n)
	}
	return n, err
}

func (trc *throttledReadCloser) Close() error { return trc.rc.Close() }

// GetCapacity delegates to the inner adapter unchanged. Capacity is a
// metadata operation and is not subject to the throttle's byte budget.
//
// This is the final form (no further changes in Task 7).
func (t *throttledAdapter) GetCapacity(ctx context.Context) (Capacity, error) {
	return t.inner.GetCapacity(ctx)
}

// Usage delegates to the inner adapter unchanged. Like GetCapacity, this is
// a metadata operation not subject to the bandwidth throttle.
func (t *throttledAdapter) Usage() (free, total int64, err error) {
	return t.inner.Usage()
}

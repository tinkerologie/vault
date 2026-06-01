package storage

import (
	"context"
	"io"
	"log"
	"math"
	"math/rand"
	"time"
)

// RetryPolicy controls the retry middleware. MaxAttempts<=1 means a single
// attempt. BaseDelay/MaxDelay of 0 make backoff instant (tests).
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryPolicy is applied to network backends.
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 30 * time.Second}

type retryAdapter struct { //nolint:unused // wired into factory in Task 7; used directly from tests
	inner  Adapter
	policy RetryPolicy
	sleep  func(time.Duration)
}

// withRetry wraps inner with a retrying adapter that uses the given policy.
// Transient errors (as determined by classify) are retried with jittered
// exponential backoff; fatal errors fail immediately.
func withRetry(inner Adapter, p RetryPolicy) Adapter { //nolint:unused // wired into factory in Task 7; used directly from tests
	if p.MaxAttempts <= 1 {
		p.MaxAttempts = 1
	}
	return &retryAdapter{inner: inner, policy: p, sleep: time.Sleep}
}

// do executes op, retrying on transient errors up to policy.MaxAttempts times.
func (r *retryAdapter) do(label string, op func() error) error { //nolint:unused
	var err error
	for attempt := 1; attempt <= r.policy.MaxAttempts; attempt++ {
		err = op()
		if err == nil {
			return nil
		}
		if !classify(err) || attempt == r.policy.MaxAttempts {
			return err
		}
		d := r.backoff(attempt)
		log.Printf("storage: %s failed (attempt %d/%d), retrying in %v: %v",
			label, attempt, r.policy.MaxAttempts, d.Truncate(time.Millisecond), err)
		r.sleep(d)
	}
	return err
}

// backoff computes the jittered exponential delay for the given attempt number
// (1-based). Returns 0 when BaseDelay is unset so tests complete instantly.
func (r *retryAdapter) backoff(attempt int) time.Duration { //nolint:unused
	if r.policy.BaseDelay <= 0 {
		return 0
	}
	exp := float64(r.policy.BaseDelay) * math.Pow(2, float64(attempt-1))
	if r.policy.MaxDelay > 0 && exp > float64(r.policy.MaxDelay) {
		exp = float64(r.policy.MaxDelay)
	}
	return time.Duration(rand.Int63n(int64(exp) + 1)) // #nosec G404 //nolint:gosec // jitter, not security
}

// Write passes through directly without retry: the caller supplies a
// one-shot io.Reader that cannot be rewound after a partial read.
func (r *retryAdapter) Write(p string, reader io.Reader) error { //nolint:unused
	return r.inner.Write(p, reader)
}

// WriteFrom retries the write on transient failures. open() is invoked once
// per attempt so each attempt starts with a fresh stream from the beginning.
func (r *retryAdapter) WriteFrom(p string, open func() (io.ReadCloser, error)) error { //nolint:unused
	return r.do("WriteFrom "+p, func() error { return r.inner.WriteFrom(p, open) })
}

func (r *retryAdapter) Read(p string) (io.ReadCloser, error) { //nolint:unused
	var rc io.ReadCloser
	err := r.do("Read "+p, func() error { var e error; rc, e = r.inner.Read(p); return e })
	return rc, err
}

func (r *retryAdapter) ReadRange(p string, off, length int64) (io.ReadCloser, error) { //nolint:unused
	var rc io.ReadCloser
	err := r.do("ReadRange "+p, func() error {
		var e error
		rc, e = r.inner.ReadRange(p, off, length)
		return e
	})
	return rc, err
}

func (r *retryAdapter) Delete(p string) error { //nolint:unused
	return r.do("Delete "+p, func() error { return r.inner.Delete(p) })
}

func (r *retryAdapter) List(prefix string) ([]FileInfo, error) { //nolint:unused
	var out []FileInfo
	err := r.do("List "+prefix, func() error { var e error; out, e = r.inner.List(prefix); return e })
	return out, err
}

func (r *retryAdapter) Stat(p string) (FileInfo, error) { //nolint:unused
	var fi FileInfo
	err := r.do("Stat "+p, func() error { var e error; fi, e = r.inner.Stat(p); return e })
	return fi, err
}

func (r *retryAdapter) TestConnection() error { //nolint:unused
	return r.do("TestConnection", func() error { return r.inner.TestConnection() })
}

// GetCapacity passes through without retry; callers control deadline via ctx.
func (r *retryAdapter) GetCapacity(ctx context.Context) (Capacity, error) { //nolint:unused
	return r.inner.GetCapacity(ctx)
}

// Usage passes through without retry; it is a lightweight probe.
func (r *retryAdapter) Usage() (int64, int64, error) { return r.inner.Usage() } //nolint:unused

package storage

import (
	"bytes"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type fakeAdapter struct {
	mockAdapter
	writeErrs  []error
	writeCalls int32
	opens      int32
}

func (f *fakeAdapter) Write(_ string, r io.Reader) error {
	_, _ = io.Copy(io.Discard, r)
	n := atomic.AddInt32(&f.writeCalls, 1)
	if int(n) <= len(f.writeErrs) {
		return f.writeErrs[n-1]
	}
	return nil
}

func (f *fakeAdapter) WriteFrom(p string, open func() (io.ReadCloser, error)) error {
	rc, err := open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return f.Write(p, rc)
}

func TestRetryWriteFromSucceedsAfterTransient(t *testing.T) {
	f := &fakeAdapter{writeErrs: []error{io.ErrUnexpectedEOF, io.ErrUnexpectedEOF}}
	a := withRetry(f, RetryPolicy{MaxAttempts: 5})
	err := a.WriteFrom("p", func() (io.ReadCloser, error) {
		atomic.AddInt32(&f.opens, 1)
		return io.NopCloser(bytes.NewReader([]byte("data"))), nil
	})
	if err != nil {
		t.Fatalf("WriteFrom = %v, want nil after retries", err)
	}
	if f.writeCalls != 3 {
		t.Errorf("writeCalls = %d, want 3", f.writeCalls)
	}
	if f.opens != 3 {
		t.Errorf("opens = %d, want 3 (reopen per attempt)", f.opens)
	}
}

func TestRetryStopsOnFatal(t *testing.T) {
	fatal := errors.New("403 forbidden")
	f := &fakeAdapter{writeErrs: []error{fatal}}
	a := withRetry(f, RetryPolicy{MaxAttempts: 5})
	err := a.WriteFrom("p", func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	})
	if !errors.Is(err, fatal) {
		t.Fatalf("err = %v, want fatal", err)
	}
	if f.writeCalls != 1 {
		t.Errorf("writeCalls = %d, want 1 (no retry on fatal)", f.writeCalls)
	}
}

func TestRetryWriteIsOneShot(t *testing.T) {
	// Plain Write must not be retried even for transient errors.
	f := &fakeAdapter{writeErrs: []error{io.ErrUnexpectedEOF}}
	a := withRetry(f, RetryPolicy{MaxAttempts: 5})
	err := a.Write("p", bytes.NewReader([]byte("data")))
	if err == nil {
		t.Fatal("Write should return the error, not retry")
	}
	if f.writeCalls != 1 {
		t.Errorf("writeCalls = %d, want 1 (one-shot)", f.writeCalls)
	}
}

func TestRetryMaxAttemptsExhausted(t *testing.T) {
	// All attempts fail with a transient error — last error must be returned.
	transient := io.ErrUnexpectedEOF
	f := &fakeAdapter{writeErrs: []error{transient, transient, transient}}
	a := withRetry(f, RetryPolicy{MaxAttempts: 3})
	err := a.WriteFrom("p", func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	})
	if !errors.Is(err, transient) {
		t.Fatalf("err = %v, want transient after exhaustion", err)
	}
	if f.writeCalls != 3 {
		t.Errorf("writeCalls = %d, want exactly MaxAttempts=3", f.writeCalls)
	}
}

func TestRetrySleepIsOverridable(t *testing.T) {
	// Confirm the sleep function is actually called between retries so it
	// can be replaced in tests.  We inject a recording no-op.
	var sleepCalls int
	f := &fakeAdapter{writeErrs: []error{io.ErrUnexpectedEOF, io.ErrUnexpectedEOF}}
	ra := &retryAdapter{
		inner:  f,
		policy: RetryPolicy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 30 * time.Second},
		sleep:  func(time.Duration) { sleepCalls++ },
	}
	err := ra.WriteFrom("p", func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("hi"))), nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sleepCalls != 2 {
		t.Errorf("sleepCalls = %d, want 2 (one between each retry)", sleepCalls)
	}
}

func TestRetryZeroBaseDelayIsInstant(t *testing.T) {
	// BaseDelay=0 means no sleep; the test completes in < 100 ms even with
	// multiple retries.
	f := &fakeAdapter{writeErrs: []error{io.ErrUnexpectedEOF, io.ErrUnexpectedEOF}}
	a := withRetry(f, RetryPolicy{MaxAttempts: 5}) // BaseDelay defaults to 0
	start := time.Now()
	err := a.WriteFrom("p", func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("data"))), nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("took %s; expected instant with zero BaseDelay", elapsed)
	}
}

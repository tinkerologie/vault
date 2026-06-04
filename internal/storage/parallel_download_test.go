package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// rangeAdapter serves ReadRange from a byte slice. If failAtOffset>0 it makes
// the part starting at that offset error once (mid-stream), to exercise retry.
type rangeAdapter struct {
	mockAdapter
	data         []byte
	failAtOffset int64
	failedOnce   atomic.Bool
}

func (a *rangeAdapter) ReadRange(_ string, offset, length int64) (io.ReadCloser, error) {
	end := offset + length
	if end > int64(len(a.data)) {
		end = int64(len(a.data))
	}
	slice := a.data[offset:end]
	if a.failAtOffset > 0 && offset == a.failAtOffset && a.failedOnce.CompareAndSwap(false, true) {
		return io.NopCloser(&errMidReader{data: slice}), nil
	}
	return io.NopCloser(bytes.NewReader(slice)), nil
}

// errMidReader returns half its data then a transient error.
type errMidReader struct {
	data []byte
	pos  int
}

func (r *errMidReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data)/2 {
		return 0, &retryableError{err: errors.New("reset")}
	}
	n := copy(p, r.data[r.pos:len(r.data)/2])
	r.pos += n
	return n, nil
}

func downloadToTemp(t *testing.T, a Adapter, size, partSize int64, conc int) []byte {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "dl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got int64
	if err := ParallelRangeDownload(t.Context(), a, "obj", f, size, partSize, conc,
		func(n int64) { atomic.AddInt64(&got, n) }); err != nil {
		t.Fatalf("ParallelRangeDownload: %v", err)
	}
	// onBytes must report at least the full object; a retried part may push it
	// slightly over (documented over-count), so assert >=.
	if atomic.LoadInt64(&got) < size {
		t.Errorf("onBytes total = %d, want >= %d", got, size)
	}
	out, err := os.ReadFile(filepath.Clean(f.Name()))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParallelRangeDownloadAssembly(t *testing.T) {
	data := bytes.Repeat([]byte("ABCDEFGH"), 4096) // 32 KiB, not part-aligned below
	for _, tc := range []struct {
		name     string
		partSize int64
		conc     int
	}{
		{"multi-part-uneven", 5000, 4},
		{"single-part", 1 << 20, 1},
		{"tiny-parts", 1, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &rangeAdapter{data: data}
			got := downloadToTemp(t, a, int64(len(data)), tc.partSize, tc.conc)
			if !bytes.Equal(got, data) {
				t.Fatalf("mismatch: got %d bytes", len(got))
			}
		})
	}
}

func TestParallelRangeDownloadRetriesPart(t *testing.T) {
	// Make the per-part backoff instant so the retry path doesn't sleep ~1s.
	// These storage tests don't run in parallel, so the save/restore is safe.
	orig := parallelDownloadPolicy
	parallelDownloadPolicy = RetryPolicy{MaxAttempts: 5} // BaseDelay 0 → instant
	t.Cleanup(func() { parallelDownloadPolicy = orig })

	data := bytes.Repeat([]byte("Z"), 30000)
	a := &rangeAdapter{data: data, failAtOffset: 10000}
	got := downloadToTemp(t, a, int64(len(data)), 10000, 3)
	if !bytes.Equal(got, data) {
		t.Fatalf("mismatch after part retry: got %d bytes", len(got))
	}
}

func TestParallelRangeDownloadZeroSize(t *testing.T) {
	a := &rangeAdapter{data: nil}
	got := downloadToTemp(t, a, 0, 1<<20, 4)
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(got))
	}
}

func TestParallelRangeDownloadCancelledCtx(t *testing.T) {
	data := bytes.Repeat([]byte("Q"), 30000)
	a := &rangeAdapter{data: data}
	f, err := os.CreateTemp(t.TempDir(), "dl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the call → must abort immediately

	err = ParallelRangeDownload(ctx, a, "obj", f, int64(len(data)), 10000, 3, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// permanentAdapter records ReadRange calls and always returns a non-transient
// error (classify → false), so the part must fail fast without retrying.
type permanentAdapter struct {
	mockAdapter
	calls atomic.Int64
}

func (a *permanentAdapter) ReadRange(_ string, _, _ int64) (io.ReadCloser, error) {
	a.calls.Add(1)
	return nil, errors.New("permanent")
}

func TestParallelRangeDownloadNonTransientFailsFast(t *testing.T) {
	a := &permanentAdapter{}
	f, err := os.CreateTemp(t.TempDir(), "dl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Single part so the call count maps directly to attempts for that part.
	err = ParallelRangeDownload(t.Context(), a, "obj", f, 5000, 1<<20, 1, nil)
	if err == nil {
		t.Fatal("expected error from non-transient failure, got nil")
	}
	if got := a.calls.Load(); got != 1 {
		t.Fatalf("expected ReadRange called once (no retry), got %d", got)
	}
}

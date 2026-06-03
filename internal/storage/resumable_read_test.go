package storage

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// flakyAdapter serves ReadRange from a fixed byte slice but injects a transient
// error after `failAfter` bytes on the first `failTimes` opens, simulating a
// connection drop mid-download. It records how many ReadRange calls happened.
type flakyAdapter struct {
	mockAdapter
	data      []byte
	failAfter int
	failTimes int
	opens     int
}

func (f *flakyAdapter) ReadRange(_ string, offset, length int64) (io.ReadCloser, error) {
	f.opens++
	if offset < 0 || offset > int64(len(f.data)) {
		return nil, errors.New("bad offset")
	}
	end := offset + length
	if end > int64(len(f.data)) {
		end = int64(len(f.data))
	}
	slice := f.data[offset:end]
	fail := f.opens <= f.failTimes
	return &flakyStream{data: slice, failAfter: f.failAfter, fail: fail}, nil
}

type flakyStream struct {
	data      []byte
	pos       int
	failAfter int
	fail      bool
}

func (s *flakyStream) Read(p []byte) (int, error) {
	if s.fail && s.pos >= s.failAfter {
		// Transient drop: ECONNRESET is classified retryable.
		return 0, &retryableError{err: errors.New("connection reset")}
	}
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	if s.fail && s.pos+n > s.failAfter {
		n = s.failAfter - s.pos
	}
	s.pos += n
	return n, nil
}

func (s *flakyStream) Close() error { return nil }

func TestResumableReaderResumesAfterMidStreamDrop(t *testing.T) {
	want := bytes.Repeat([]byte("vault-restore-"), 5000) // ~70 KB
	fa := &flakyAdapter{data: want, failAfter: 1000, failTimes: 1}
	policy := RetryPolicy{MaxAttempts: 5} // BaseDelay 0 → instant backoff

	rr := NewResumableReader(t.Context(), fa, "obj", int64(len(want)), policy)
	got, err := io.ReadAll(rr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(want))
	}
	if fa.opens < 2 {
		t.Fatalf("expected at least one resume (>=2 opens), got %d", fa.opens)
	}
	if err := rr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestResumableReaderBoundedByMaxAttempts(t *testing.T) {
	want := bytes.Repeat([]byte("x"), 10000)
	// Always fails at byte 0 → no forward progress → must give up.
	fa := &flakyAdapter{data: want, failAfter: 0, failTimes: 1000}
	rr := NewResumableReader(t.Context(), fa, "obj", int64(len(want)), RetryPolicy{MaxAttempts: 3})
	_, err := io.ReadAll(rr)
	if err == nil {
		t.Fatal("expected error after exhausting attempts, got nil")
	}
	if fa.opens > 4 { // 3 attempts + small slack
		t.Fatalf("expected bounded opens (~3), got %d", fa.opens)
	}
}

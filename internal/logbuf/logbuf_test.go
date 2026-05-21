package logbuf

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestRingSnapshotBelowCapacity(t *testing.T) {
	r := New(64)
	_, _ = r.Write([]byte("hello "))
	_, _ = r.Write([]byte("world"))
	if got := string(r.Snapshot()); got != "hello world" {
		t.Fatalf("Snapshot = %q, want %q", got, "hello world")
	}
}

func TestRingSnapshotAfterWrap(t *testing.T) {
	r := New(8)
	_, _ = r.Write([]byte("abcdefghij")) // 10 bytes into 8-byte ring — first 2 evicted
	if got := string(r.Snapshot()); got != "cdefghij" {
		t.Fatalf("Snapshot = %q, want %q (oldest 2 bytes should be evicted)", got, "cdefghij")
	}
}

func TestRingSnapshotExactCapacity(t *testing.T) {
	r := New(5)
	_, _ = r.Write([]byte("abcde"))
	if got := string(r.Snapshot()); got != "abcde" {
		t.Fatalf("Snapshot = %q, want %q", got, "abcde")
	}
	// One more byte should evict 'a'.
	_, _ = r.Write([]byte("f"))
	if got := string(r.Snapshot()); got != "bcdef" {
		t.Fatalf("after one-byte wrap, Snapshot = %q, want %q", got, "bcdef")
	}
}

func TestRingZeroCapacityIsNoop(t *testing.T) {
	r := New(0)
	n, err := r.Write([]byte("ignored"))
	if err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if n != len("ignored") {
		t.Fatalf("Write returned n=%d, want %d (must report full length to avoid short-write panics in MultiWriter)", n, len("ignored"))
	}
	if snap := r.Snapshot(); snap != nil {
		t.Fatalf("Snapshot from zero-cap ring = %q, want nil", snap)
	}
}

func TestRingNilSafe(t *testing.T) {
	var r *Ring
	n, err := r.Write([]byte("nope"))
	if err != nil || n != 4 {
		t.Fatalf("nil ring Write = (%d, %v), want (4, nil)", n, err)
	}
	if r.Snapshot() != nil {
		t.Fatal("nil ring Snapshot must return nil")
	}
}

func TestRingWriteReturnsOriginalLength(t *testing.T) {
	// MultiWriter aborts the chain if any writer reports a short write
	// (io.ErrShortWrite). The ring must always report len(p).
	r := New(4)
	mw := io.MultiWriter(io.Discard, r)
	if _, err := mw.Write([]byte("oversized payload")); err != nil {
		t.Fatalf("MultiWriter Write error = %v", err)
	}
}

func TestRingConcurrentWrites(t *testing.T) {
	r := New(1024)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 64; j++ {
				_, _ = r.Write([]byte("xxxxx\n"))
			}
		}(i)
	}
	wg.Wait()
	snap := r.Snapshot()
	// Every byte must be 'x' or '\n' — no torn writes / interleaved garbage.
	for _, b := range snap {
		if b != 'x' && b != '\n' {
			t.Fatalf("snapshot contained unexpected byte %q after concurrent writes", b)
		}
	}
	// And we should have consumed exactly the buffer capacity (since
	// total bytes written 16*64*6 = 6144 > 1024 cap).
	if got, want := len(snap), 1024; got != want {
		t.Fatalf("Snapshot length = %d, want %d", got, want)
	}
}

func TestRingViaStandardLogger(t *testing.T) {
	// Real-world wiring: log.SetOutput(io.MultiWriter(stderr, ring)).
	r := New(256)
	var stderr bytes.Buffer
	mw := io.MultiWriter(&stderr, r)
	mw.Write([]byte("2026/05/21 10:00:00 hello from logger\n"))
	snap := string(r.Snapshot())
	if !strings.Contains(snap, "hello from logger") {
		t.Fatalf("ring did not capture logger output: %q", snap)
	}
	if stderr.Len() == 0 {
		t.Fatal("stderr passthrough lost the line")
	}
}

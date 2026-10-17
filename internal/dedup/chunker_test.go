package dedup

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
)

func TestChunkerDeterminism(t *testing.T) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 4<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}

	b1 := collectBoundaries(t, secret, data)
	b2 := collectBoundaries(t, secret, data)
	if len(b1) != len(b2) {
		t.Fatalf("len mismatch: %d vs %d", len(b1), len(b2))
	}
	for i := range b1 {
		if b1[i] != b2[i] {
			t.Fatalf("boundary %d differs: %d vs %d", i, b1[i], b2[i])
		}
	}
}

func TestChunkerSecretAffectsBoundaries(t *testing.T) {
	s1 := bytes.Repeat([]byte{0x01}, 32)
	s2 := bytes.Repeat([]byte{0x02}, 32)
	data := make([]byte, 4<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	b1 := collectBoundaries(t, s1, data)
	b2 := collectBoundaries(t, s2, data)
	if len(b1) == 0 || len(b2) == 0 {
		t.Fatal("no chunks produced")
	}
	overlap := 0
	set := make(map[int]struct{}, len(b1))
	for _, v := range b1 {
		set[v] = struct{}{}
	}
	for _, v := range b2 {
		if _, ok := set[v]; ok {
			overlap++
		}
	}
	if overlap*2 >= len(b1) {
		t.Fatalf("too much boundary overlap (%d/%d); secret should change boundaries", overlap, len(b1))
	}
}

func TestChunkerEdgeSizes(t *testing.T) {
	secret := bytes.Repeat([]byte{0xab}, 32)
	cases := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"one_byte", 1},
		{"below_min", 100 * 1024},
		{"at_min", 256 * 1024},
		{"at_avg", 1024 * 1024},
		{"at_max", 4 * 1024 * 1024},
		{"above_max", 4*1024*1024 + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, tc.size)
			chunker, err := NewChunker(secret)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			err = chunker.Split(bytes.NewReader(data), func(chunk []byte) error {
				n++
				return nil
			})
			if err != nil && err != io.EOF {
				t.Fatal(err)
			}
			if tc.size == 0 && n != 0 {
				t.Fatalf("empty input produced %d chunks", n)
			}
			if tc.size > 0 && n == 0 {
				t.Fatal("non-empty input produced 0 chunks")
			}
		})
	}
}

func collectBoundaries(t *testing.T, secret, data []byte) []int {
	t.Helper()
	chunker, err := NewChunker(secret)
	if err != nil {
		t.Fatal(err)
	}
	boundaries := []int{}
	cum := 0
	err = chunker.Split(bytes.NewReader(data), func(chunk []byte) error {
		cum += len(chunk)
		boundaries = append(boundaries, cum)
		return nil
	})
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	return boundaries
}

// TestChunkerSplitReusesScanBuffer covers issue #500: Split borrows its
// 8 MiB scan buffer from a pool instead of allocating one per call.
func TestChunkerSplitReusesScanBuffer(t *testing.T) {
	if raceEnabled {
		// sync.Pool drops a random share of Puts under -race, so pooled
		// buffers are reallocated and the per-call figure is meaningless.
		t.Skip("allocation counts are not stable under -race")
	}
	c, err := NewChunker(make([]byte, SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	small := bytes.Repeat([]byte{0xab}, 16<<10)
	split := func() {
		if err := c.Split(bytes.NewReader(small), func([]byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	split() // warm the pool
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	const calls = 20
	for range calls {
		split()
	}
	runtime.ReadMemStats(&after)
	// Without the pool each call allocates 2×ChunkMax (8 MiB). A GC may still
	// drop a pooled buffer, so allow the odd re-allocation.
	if perCall := (after.TotalAlloc - before.TotalAlloc) / calls; perCall > 1<<20 {
		t.Fatalf("Split allocated %d bytes per call; the scan buffer should be pooled", perCall)
	}
}

// TestReleaseScanBufferClearsWhatWasRead checks the clearing rule Split
// relies on, on a buffer the test holds, so it does not depend on which
// buffer the pool hands out next.
func TestReleaseScanBufferClearsWhatWasRead(t *testing.T) {
	for _, read := range []int64{0, 1, 64 << 10, 2 * ChunkMax, 3 * ChunkMax} {
		buf := scanBuffers.Get()
		fill := min(read, int64(len(*buf)))
		for i := range fill {
			(*buf)[i] = 0xcd
		}
		releaseScanBuffer(buf, read)
		for i, b := range *buf {
			if b != 0 {
				t.Fatalf("read=%d: byte %d = %#x after release, want 0", read, i, b)
			}
		}
	}
}

func TestChunkerSplitCountsBytesOnReadError(t *testing.T) {
	c, err := NewChunker(make([]byte, SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	r := io.MultiReader(bytes.NewReader(bytes.Repeat([]byte{0xcd}, 64<<10)), iotest.ErrReader(errors.New("disk gone")))
	if err := c.Split(r, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "disk gone") {
		t.Fatalf("want the read error, got %v", err)
	}
	cr := &countingReader{r: bytes.NewReader(make([]byte, 100))}
	if n, _ := io.Copy(io.Discard, cr); n != 100 || cr.n != 100 {
		t.Fatalf("countingReader counted %d of %d bytes", cr.n, n)
	}
}

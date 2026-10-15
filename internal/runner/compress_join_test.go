package runner

import (
	"io"
	"testing"
)

// countingSource counts bytes read with a plain (unsynchronised) counter, so
// the race detector flags any read that overlaps the caller's reset.
type countingSource struct {
	n   int
	src io.Reader
}

func (c *countingSource) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.n += n
	return n, err
}

// TestTransportCompressReaderCloseJoinsCompressor guards #456: Close must not
// return while the compressor goroutine can still read the source, or a
// retried upload races with the previous attempt's byte counting.
func TestTransportCompressReaderCloseJoinsCompressor(t *testing.T) {
	for _, compression := range []string{"gzip", "zstd"} {
		t.Run(compression, func(t *testing.T) {
			src := &countingSource{src: io.LimitReader(zeroReader{}, 64<<20)}
			rc, err := transportCompressReader(compression, src)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.CopyN(io.Discard, rc, 1024); err != nil {
				t.Fatal(err)
			}
			if err := rc.Close(); err != nil {
				t.Fatal(err)
			}
			src.n = 0 // the retry path resets its counter here
			if err := rc.Close(); err != nil {
				t.Fatalf("second Close: %v", err)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

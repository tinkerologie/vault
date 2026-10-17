// Package dedup implements per-destination content-defined deduplication.
package dedup

import (
	"fmt"
	"io"

	chunkers "github.com/PlakarKorp/go-cdc-chunkers"
	// Blank import registers both "fastcdc" and "kfastcdc" algorithms
	// (the keyed variant lives in the same package as the unkeyed one).
	_ "github.com/PlakarKorp/go-cdc-chunkers/chunkers/fastcdc"

	"github.com/tinkerologie/vault/internal/bufpool"
)

// Default chunk size parameters. 256 KiB / 1 MiB / 4 MiB matches the spec's
// chosen min/avg/max for typical Unraid workloads (Immich, Nextcloud,
// container volumes) — good dedup ratio without metadata bloat.
const (
	ChunkMin = 256 * 1024
	ChunkAvg = 1 * 1024 * 1024
	ChunkMax = 4 * 1024 * 1024
)

// SecretSize is the required length of the chunker secret in bytes.
// Keyed-FastCDC uses BLAKE3 keyed mode under the hood, which mandates a
// 32-byte key.
const SecretSize = 32

// algorithm is the registered name of the keyed FastCDC variant inside the
// go-cdc-chunkers library. Do NOT change to "fastcdc" — that's the unkeyed
// algorithm and would silently break our fingerprinting-resistance guarantee.
const algorithm = "kfastcdc"

// Chunker streams an io.Reader through Keyed-FastCDC, invoking a callback
// for every variable-length chunk. The secret seeds the Gear table (via a
// BLAKE3-keyed PRF over the default gear) so chunk boundaries are
// non-deterministic to observers without it — this closes the
// fingerprinting-attack class described in Truong et al. 2025.
type Chunker struct {
	secret []byte
}

// NewChunker constructs a chunker bound to the given 32-byte secret. The
// secret is copied defensively so callers may zero or reuse their buffer.
func NewChunker(secret []byte) (*Chunker, error) {
	if len(secret) != SecretSize {
		return nil, fmt.Errorf("dedup: chunker secret must be %d bytes, got %d", SecretSize, len(secret))
	}
	cpy := make([]byte, SecretSize)
	copy(cpy, secret)
	return &Chunker{secret: cpy}, nil
}

// Split reads r to EOF and invokes cb with each chunk's bytes. The slice
// passed to cb is only valid for the duration of the call — cb must copy
// before retaining (the underlying buffer is reused on the next Next()).
// Returns the first non-EOF error from r or cb. Empty inputs produce zero
// chunk callbacks.
func (c *Chunker) Split(r io.Reader, cb func(chunk []byte) error) error {
	opts := &chunkers.ChunkerOpts{
		MinSize:    ChunkMin,
		MaxSize:    ChunkMax,
		NormalSize: ChunkAvg,
		Key:        c.secret,
	}
	// chunkers.NewChunker allocates a fresh 2×ChunkMax (8 MiB) scan buffer,
	// and Split runs once per file: a folder of 40,000 small files made
	// 40,000 such allocations and kept the GC busy for ~40% of the backup's
	// CPU (issue #500). Borrow the buffer instead. The reader only ever
	// writes below the number of bytes it has read, so clearing that prefix
	// on return wipes all file content from it.
	buf := scanBuffers.Get()
	src := &countingReader{r: r}
	defer func() { releaseScanBuffer(buf, src.n) }() // src.n read at return, not now
	ch, err := chunkers.NewChunkerBuffer(algorithm, src, opts, *buf)
	if err != nil {
		return fmt.Errorf("dedup: new %s chunker: %w", algorithm, err)
	}
	for {
		chunk, err := ch.Next()
		if len(chunk) > 0 {
			if cbErr := cb(chunk); cbErr != nil {
				return cbErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("dedup: chunker next: %w", err)
		}
	}
}

// scanBuffers pools Split's scan buffers, sized like the library's own
// default (2 × ChunkMax) to keep its read stride.
var scanBuffers = bufpool.New(2 * ChunkMax)

// releaseScanBuffer returns buf to the pool after clearing the prefix a
// reader that consumed read bytes can have written into it.
func releaseScanBuffer(buf *[]byte, read int64) {
	scanBuffers.PutUsed(buf, int(min(read, int64(len(*buf)))))
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

// Read reads from the wrapped reader and adds the byte count to n.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

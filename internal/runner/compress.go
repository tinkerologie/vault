package runner

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
)

var (
	_ = decompressReader
	_ = fileCompressionExt
)

// gzipMagic and zstdMagic are the leading bytes of each codec's container
// format. We use them to decide whether a downloaded file actually needs to be
// transport-decompressed during restore.
var (
	gzipMagic = []byte{0x1f, 0x8b}
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
)

// compressWriter was removed in favour of engine-side compression. The engine
// now owns archive-level compression so the runner only handles encryption and
// transport. See internal/engine/compress.go for the replacement helpers.

// decompressReader wraps a reader with the appropriate decompression based on
// the file extension. Returns the decompressed reader and a close function.
// If no compression extension is detected, returns the original reader.
func decompressReader(r io.Reader, fileName string) (io.Reader, func() error, error) {
	ext := fileCompressionExt(fileName)
	switch ext {
	case ".gz":
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("creating gzip reader: %w", err)
		}
		return gr, gr.Close, nil

	case ".zst":
		zr, err := zstd.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("creating zstd reader: %w", err)
		}
		return zr, func() error { zr.Close(); return nil }, nil

	default:
		return r, func() error { return nil }, nil
	}
}

// decompressStoredReader unwraps one layer of transport compression from a
// restored file. The engine is the single source of truth for archive-level
// compression since 2026.05.03, so newly produced backups never get a
// transport wrap and must be left untouched here (otherwise plain files such
// as config.json fail with "magic number mismatch"). For legacy backups
// produced before that change — where the runner double-wrapped every upload
// with the job's configured codec — this function still strips the outer
// layer when the bytes really are compressed.
//
// Decisions are made by peeking the first four bytes against gzip/zstd magic
// numbers, never by trusting the filename extension or the job's compression
// setting. The corresponding extension is stripped from the returned name
// only if a layer was actually peeled.
func decompressStoredReader(r io.Reader, fileName, compression string) (io.Reader, func() error, string, error) {
	_ = compression // retained for API stability; detection is content-based.

	br := bufio.NewReaderSize(r, 4096)
	peek, err := br.Peek(4)
	if err != nil && err != io.EOF {
		return nil, nil, "", fmt.Errorf("peeking %s: %w", fileName, err)
	}

	switch {
	case len(peek) >= 2 && bytes.Equal(peek[:2], gzipMagic):
		gr, gerr := gzip.NewReader(br)
		if gerr != nil {
			return nil, nil, "", fmt.Errorf("creating gzip reader: %w", gerr)
		}
		return gr, gr.Close, strings.TrimSuffix(fileName, ".gz"), nil
	case len(peek) >= 4 && bytes.Equal(peek[:4], zstdMagic):
		zr, zerr := zstd.NewReader(br)
		if zerr != nil {
			return nil, nil, "", fmt.Errorf("creating zstd reader: %w", zerr)
		}
		return zr, func() error { zr.Close(); return nil }, strings.TrimSuffix(fileName, ".zst"), nil
	default:
		return br, func() error { return nil }, fileName, nil
	}
}

// fileCompressionExt returns the compression extension from a file name,
// stripping any trailing ".age" encryption extension first.
func fileCompressionExt(name string) string {
	// Strip encryption extension first.
	if len(name) > 4 && name[len(name)-4:] == ".age" {
		name = name[:len(name)-4]
	}
	if len(name) > 3 && name[len(name)-3:] == ".gz" {
		return ".gz"
	}
	if len(name) > 4 && name[len(name)-4:] == ".zst" {
		return ".zst"
	}
	return ""
}

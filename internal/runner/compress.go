package runner

import (
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

// decompressStoredReader unwraps the transport compression configured for the
// job. This avoids misclassifying intrinsic artifact extensions like .tar.gz
// as transport compression when the job itself was stored with compression=none.
func decompressStoredReader(r io.Reader, fileName, compression string) (io.Reader, func() error, string, error) {
	switch compression {
	case "gzip":
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, "", fmt.Errorf("creating gzip reader: %w", err)
		}
		return gr, gr.Close, strings.TrimSuffix(fileName, ".gz"), nil
	case "zstd":
		zr, err := zstd.NewReader(r)
		if err != nil {
			return nil, nil, "", fmt.Errorf("creating zstd reader: %w", err)
		}
		return zr, func() error { zr.Close(); return nil }, strings.TrimSuffix(fileName, ".zst"), nil
	default:
		return r, func() error { return nil }, fileName, nil
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

package runner

import (
	"compress/gzip"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// compressWriter wraps a writer with the specified compression and returns the
// compressed writer, a close function that must be called to flush/finalize
// the compression, and the file extension to append (e.g. ".gz", ".zst").
// For "none" or empty compression, returns the original writer unchanged.
func compressWriter(w io.Writer, compression string) (io.Writer, func() error, string, error) {
	switch compression {
	case "gzip":
		gw, err := gzip.NewWriterLevel(w, gzip.DefaultCompression)
		if err != nil {
			return nil, nil, "", fmt.Errorf("creating gzip writer: %w", err)
		}
		return gw, gw.Close, ".gz", nil

	case "zstd":
		zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			return nil, nil, "", fmt.Errorf("creating zstd writer: %w", err)
		}
		return zw, zw.Close, ".zst", nil

	case "none", "":
		return w, func() error { return nil }, "", nil

	default:
		return nil, nil, "", fmt.Errorf("unknown compression type: %s", compression)
	}
}

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

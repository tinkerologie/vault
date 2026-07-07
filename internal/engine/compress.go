package engine

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// findArchive locates a tar archive in dir whose name matches base with one
// of the supported compression suffixes (plain, .gz, .zst). It tries the
// plain name first (matches archives produced by the new compression-aware
// engine after a "none" job, or by the runner after stripping the transport
// compression for "gzip"/"zstd" jobs) and falls back to the legacy ".tar.gz"
// suffix so existing on-storage archives produced by older versions of Vault
// continue to restore correctly.
func findArchive(dir, base string) (string, error) {
	candidates := []string{
		base,         // e.g. volume_0.tar / image.tar
		base + ".gz", // legacy or gzip jobs after runner-side decompress strip
		base + ".zst",
	}
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("archive %s (with any supported compression suffix) not found in %s", base, dir)
}

// Compression algorithms supported by the engine when writing archive files.
// The runner-level transport compression is being phased out in favour of
// these engine-native modes so users get exactly what they asked for in the
// UI ("None" really means no compression).
const (
	CompressionNone = "none"
	CompressionGzip = "gzip"
	CompressionZstd = "zstd"
)

// Compression levels let users trade time for size. They are encoded onto the
// compression value as an "algo:level" suffix (e.g. "zstd:best") that travels
// opaquely through the engine as job.Compression — only splitCompression,
// archiveExt and MaybeDowngradeCompression interpret it. A bare "zstd"/"gzip"
// (no suffix) means the algorithm's default level, preserving prior behaviour.
const (
	CompressionLevelFastest = "fastest"
	CompressionLevelDefault = "default"
	CompressionLevelBetter  = "better"
	CompressionLevelBest    = "best"
)

// splitCompression separates an "algo:level" compression spec into its parts.
// A spec with no ":" yields an empty level (the algorithm default).
func splitCompression(spec string) (algo, level string) {
	if i := strings.IndexByte(spec, ':'); i >= 0 {
		return spec[:i], spec[i+1:]
	}
	return spec, ""
}

// JoinCompression builds an "algo:level" spec. The default level (or an empty
// algo/level) produces the bare algo so existing "gzip"/"zstd"/"none" values —
// and their on-storage archives — are byte-for-byte unchanged. The runner uses
// this to fold a job's compression_level into the compression value it hands
// the engine.
func JoinCompression(algo, level string) string {
	if level == "" || level == CompressionLevelDefault {
		return algo
	}
	switch algo {
	case CompressionGzip, CompressionZstd:
		return algo + ":" + level
	default:
		// "none"/unknown algorithms carry no level.
		return algo
	}
}

// archiveExt returns the filename suffix the engine appends after `.tar` for
// the given compression spec. Returns "" for none/empty/unknown.
func archiveExt(compression string) string {
	algo, _ := splitCompression(compression)
	switch algo {
	case CompressionGzip:
		return ".gz"
	case CompressionZstd:
		return ".zst"
	default:
		return ""
	}
}

// zstdEncoderLevel maps a user level name to a zstd encoder level.
func zstdEncoderLevel(level string) zstd.EncoderLevel {
	switch level {
	case CompressionLevelFastest:
		return zstd.SpeedFastest
	case CompressionLevelBetter:
		return zstd.SpeedBetterCompression
	case CompressionLevelBest:
		return zstd.SpeedBestCompression
	default:
		return zstd.SpeedDefault
	}
}

// gzipEncoderLevel maps a user level name to a compress/gzip level.
func gzipEncoderLevel(level string) int {
	switch level {
	case CompressionLevelFastest:
		return gzip.BestSpeed
	case CompressionLevelBetter:
		return 7
	case CompressionLevelBest:
		return gzip.BestCompression
	default:
		return gzip.DefaultCompression
	}
}

// compressedWriter wraps w with the chosen compression algorithm and level
// (parsed from an "algo:level" spec). The returned closer flushes and finalises
// the compression stream and MUST be called before the underlying writer is
// closed.
//
// For an empty / "none" / unknown compression value, the original writer is
// returned with a no-op closer so callers can always defer the close.
func compressedWriter(w io.Writer, compression string) (io.Writer, func() error, error) {
	algo, level := splitCompression(compression)
	switch algo {
	case CompressionGzip:
		gw, err := gzip.NewWriterLevel(w, gzipEncoderLevel(level))
		if err != nil {
			return nil, nil, fmt.Errorf("creating gzip writer: %w", err)
		}
		return gw, gw.Close, nil
	case CompressionZstd:
		zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstdEncoderLevel(level)))
		if err != nil {
			return nil, nil, fmt.Errorf("creating zstd writer: %w", err)
		}
		return zw, zw.Close, nil
	default:
		// None / empty / unknown: pass-through.
		return w, func() error { return nil }, nil
	}
}

// gzipMagic and zstdMagic are the well-known frame magic numbers we peek at
// the start of an archive to determine its compression layer regardless of
// filename. This keeps the format detection robust against rename / .age
// stripping confusion.
var (
	gzipMagic = []byte{0x1f, 0x8b}
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
)

// detectingReader sniffs the first few bytes of r and returns a reader that
// transparently decompresses gzip or zstd streams. Plain (uncompressed) inputs
// are passed through unchanged. The returned closer must be called to release
// decompressor resources.
//
// This makes the engine forward-compatible with archives produced by any of
// the runner's old (engine=always gzip + runner transport-wrap) format
// combinations: `.tar`, `.tar.gz`, and `.tar.zst` all decode correctly here.
func detectingReader(r io.Reader) (io.Reader, func() error, error) {
	br := bufio.NewReader(r)
	head, err := br.Peek(4)
	if err != nil && err != io.EOF {
		return nil, nil, fmt.Errorf("peeking archive magic: %w", err)
	}

	switch {
	case len(head) >= 2 && bytes.Equal(head[:2], gzipMagic):
		gr, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, fmt.Errorf("creating gzip reader: %w", err)
		}
		return gr, gr.Close, nil
	case len(head) >= 4 && bytes.Equal(head[:4], zstdMagic):
		zr, err := zstd.NewReader(br)
		if err != nil {
			return nil, nil, fmt.Errorf("creating zstd reader: %w", err)
		}
		return zr, func() error { zr.Close(); return nil }, nil
	default:
		// Plain tar (or unknown bytes — let tar.Reader produce the error).
		return br, func() error { return nil }, nil
	}
}

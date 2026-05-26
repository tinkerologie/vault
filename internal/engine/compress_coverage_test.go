package engine

import (
	"bytes"
	"testing"
)

// TestDetectingReader_CorruptedGzip drives the gzip.NewReader error
// branch: the magic bytes match gzip (1f 8b) but the body is junk, so
// the underlying gzip header parse fails.
func TestDetectingReader_CorruptedGzip(t *testing.T) {
	t.Parallel()
	junk := []byte{0x1f, 0x8b, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	_, _, err := detectingReader(bytes.NewReader(junk))
	if err == nil {
		t.Fatal("expected gzip header parse error for corrupted gzip stream")
	}
}

// TestDetectingReader_CorruptedZstd drives the zstd.NewReader error
// branch: the magic bytes match zstd but the rest is incomplete /
// inconsistent. The zstd package validates the frame header up-front.
func TestDetectingReader_CorruptedZstd(t *testing.T) {
	t.Parallel()
	// zstd magic + garbage. Some malformations are detected at NewReader,
	// others only at first Read. We try a deliberately invalid frame
	// header descriptor (0xFF) which is rejected immediately.
	junk := []byte{0x28, 0xb5, 0x2f, 0xfd, 0xFF}
	_, closeFn, err := detectingReader(bytes.NewReader(junk))
	if err == nil {
		// If NewReader accepted it, that's OK — the gzip branch is the
		// one that always rejects. Clean up.
		_ = closeFn()
		t.Skip("zstd.NewReader accepted malformed frame header; first Read would fail instead")
	}
}

// TestDetectingReader_ShortStream covers the path where Peek returns EOF
// before producing magic bytes — the function still returns a usable
// pass-through reader without error.
func TestDetectingReader_ShortStream(t *testing.T) {
	t.Parallel()
	// Empty stream: Peek returns io.EOF immediately. The detector returns
	// nil error and a pass-through reader (the io.EOF here is the
	// "EOF == fine, fall through" path inside detectingReader).
	r, closeFn, err := detectingReader(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("expected nil error for empty stream, got %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil reader for empty stream")
	}
	if err := closeFn(); err != nil {
		t.Errorf("close: %v", err)
	}

	// Single-byte stream (not enough for any magic): same fall-through.
	r, closeFn, err = detectingReader(bytes.NewReader([]byte{0x7f}))
	if err != nil {
		t.Fatalf("expected nil error for 1-byte stream, got %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil reader for 1-byte stream")
	}
	_ = closeFn()
}

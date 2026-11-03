package engine

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/tinkerologie/vault/internal/bounded"
)

// zstdFrameDeclaringWindow is a frame header asking for a window of 1<<exp
// bytes, followed by an empty final block. It is a few bytes on storage.
func zstdFrameDeclaringWindow(exp byte) []byte {
	return []byte{
		0x28, 0xb5, 0x2f, 0xfd, // magic
		0x00,             // frame header: window descriptor present, no content size
		(exp - 10) << 3,  // window descriptor
		0x01, 0x00, 0x00, // last raw block, length 0
	}
}

// A stream declares its own window, so this pins the decoder's limit: a few
// bytes on storage must not be able to demand gigabytes of memory.
func TestDecompressingReader_RefusesHugeZstdWindow(t *testing.T) {
	t.Parallel()
	r, closeFn, err := DecompressingReader(bytes.NewReader(zstdFrameDeclaringWindow(33)))
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn() //nolint:errcheck // test
	if _, err := io.ReadAll(r); !errors.Is(err, zstd.ErrWindowSizeExceeded) {
		t.Fatalf("8 GiB window: %v", err)
	}
}

func TestReadTarIndex_RefusesOversizedSidecar(t *testing.T) {
	t.Parallel()
	body := `{"version":1,"files":[` + strings.Repeat(`{"path":"x"},`, 100) + `{"path":"x"}]}`
	if _, err := readTarIndexLimited(strings.NewReader(body), 64); !errors.Is(err, bounded.ErrTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
	idx, err := readTarIndexLimited(strings.NewReader(body), int64(len(body)))
	if err != nil || len(idx.Files) != 101 {
		t.Fatalf("at the limit: %d files, %v", len(idx.Files), err)
	}
}

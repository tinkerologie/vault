package runner

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestDecompressStoredReader_ContentBased(t *testing.T) {
	t.Parallel()

	plain := []byte(`{"hello":"world"}`)

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	_, _ = gw.Write(plain)
	_ = gw.Close()

	var zstBuf bytes.Buffer
	zw, _ := zstd.NewWriter(&zstBuf)
	_, _ = zw.Write(plain)
	_ = zw.Close()

	tests := []struct {
		name     string
		input    []byte
		fileName string
		// compression is the *job* compression setting; the new logic must
		// ignore this and decide based on the actual bytes.
		compression string
		wantName    string
		wantStrip   bool
	}{
		{"plain json on zstd job", plain, "config.json", "zstd", "config.json", false},
		{"plain json on gzip job", plain, "config.json", "gzip", "config.json", false},
		{"gzip bytes on none job", gzBuf.Bytes(), "data.gz", "none", "data", true},
		{"zstd bytes on zstd job", zstBuf.Bytes(), "data.zst", "zstd", "data", true},
		{"gzip bytes on zstd job (legacy double-wrap inner)", gzBuf.Bytes(), "data.tar.gz", "zstd", "data.tar", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, closeFn, name, err := decompressStoredReader(bytes.NewReader(tt.input), tt.fileName, tt.compression)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer func() { _ = closeFn() }()
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if tt.wantStrip {
				if !bytes.Equal(got, plain) {
					t.Errorf("expected decompressed bytes %q, got %q", plain, got)
				}
			} else {
				if !bytes.Equal(got, tt.input) {
					t.Errorf("expected unchanged bytes %q, got %q", tt.input, got)
				}
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
		})
	}
}

// VM engine artifacts (qcow2 images, domain.xml, vm_meta.json) are staged
// uncompressed — unlike container/folder archives, the engine applies no
// codec — so the upload path must honour the job's compression for them.
func TestTransportCompressReaderRoundTrip(t *testing.T) {
	t.Parallel()

	plain := bytes.Repeat([]byte("vault vm disk content "), 1024)

	for _, tt := range []struct {
		compression string
		wantSuffix  string
	}{
		{"gzip", ".gz"},
		{"zstd", ".zst"},
	} {
		t.Run(tt.compression, func(t *testing.T) {
			t.Parallel()
			suffix := transportCompressionSuffix(tt.compression)
			if suffix != tt.wantSuffix {
				t.Fatalf("suffix = %q, want %q", suffix, tt.wantSuffix)
			}

			rc, err := transportCompressReader(tt.compression, bytes.NewReader(plain))
			if err != nil {
				t.Fatalf("transportCompressReader: %v", err)
			}
			compressed, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read compressed: %v", err)
			}
			if err := rc.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if !looksCompressed(compressed) {
				t.Fatal("output does not carry a gzip/zstd magic number")
			}
			if len(compressed) >= len(plain) {
				t.Fatalf("compressible input did not shrink: %d >= %d", len(compressed), len(plain))
			}

			// The restore path must transparently peel it back off.
			dr, closeFn, name, err := decompressStoredReader(bytes.NewReader(compressed), "vdisk0.qcow2"+suffix, tt.compression)
			if err != nil {
				t.Fatalf("decompressStoredReader: %v", err)
			}
			defer func() { _ = closeFn() }()
			got, err := io.ReadAll(dr)
			if err != nil {
				t.Fatalf("read decompressed: %v", err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatal("round trip mismatch")
			}
			if name != "vdisk0.qcow2" {
				t.Fatalf("restored name = %q", name)
			}
		})
	}

	if got := transportCompressionSuffix("none"); got != "" {
		t.Fatalf("suffix for none = %q, want empty", got)
	}

	if _, err := transportCompressReader("brotli", bytes.NewReader(plain)); err == nil {
		t.Fatal("expected error for unsupported transport codec")
	}
}

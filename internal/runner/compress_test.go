package runner

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestFileCompressionExt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
	}{
		{"backup.tar.gz", ".gz"},
		{"backup.tar.zst", ".zst"},
		{"backup.tar.gz.age", ".gz"},
		{"backup.tar.zst.age", ".zst"},
		{"backup.tar", ""},
		{"backup.tar.age", ""},
		{"simple.age", ""},
		{"file.gz", ".gz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := fileCompressionExt(tt.name)
			if got != tt.want {
				t.Errorf("fileCompressionExt(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

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

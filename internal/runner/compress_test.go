package runner

import (
	"bytes"
	"io"
	"testing"
)

func TestCompressWriter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		compression string
		wantExt     string
		wantErr     bool
	}{
		{"gzip", "gzip", ".gz", false},
		{"zstd", "zstd", ".zst", false},
		{"none", "none", "", false},
		{"empty", "", "", false},
		{"unknown", "brotli", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			w, closeFn, ext, err := compressWriter(&buf, tt.compression)
			if (err != nil) != tt.wantErr {
				t.Errorf("compressWriter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if ext != tt.wantExt {
				t.Errorf("compressWriter() ext = %q, want %q", ext, tt.wantExt)
			}

			// Write data through the compressor and close.
			data := []byte("hello world")
			if _, err := w.Write(data); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if err := closeFn(); err != nil {
				t.Fatalf("closeFn() error = %v", err)
			}

			// For compression types, verify the output is non-empty.
			if tt.compression != "none" && tt.compression != "" {
				if buf.Len() == 0 {
					t.Error("expected non-empty compressed output")
				}
			}
		})
	}
}

func TestCompressDecompressRoundTrip(t *testing.T) {
	t.Parallel()

	compressions := []struct {
		name string
		ext  string
	}{
		{"gzip", ".gz"},
		{"zstd", ".zst"},
	}
	for _, cc := range compressions {
		t.Run(cc.name, func(t *testing.T) {
			t.Parallel()
			original := []byte("the quick brown fox jumps over the lazy dog")

			// Compress.
			var compressed bytes.Buffer
			w, closeFn, ext, err := compressWriter(&compressed, cc.name)
			if err != nil {
				t.Fatalf("compressWriter() error = %v", err)
			}
			if ext != cc.ext {
				t.Errorf("ext = %q, want %q", ext, cc.ext)
			}
			if _, err := w.Write(original); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if err := closeFn(); err != nil {
				t.Fatalf("closeFn() error = %v", err)
			}

			// Decompress.
			r, closeDecompress, err := decompressReader(&compressed, "file"+cc.ext)
			if err != nil {
				t.Fatalf("decompressReader() error = %v", err)
			}
			result, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if err := closeDecompress(); err != nil {
				t.Fatalf("closeDecompress() error = %v", err)
			}

			if !bytes.Equal(result, original) {
				t.Errorf("round-trip mismatch: got %q, want %q", result, original)
			}
		})
	}
}

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

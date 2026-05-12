package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// TestDetectingReader_AutoDetectsFormat confirms the auto-detect reader can
// transparently decode plain tar, gzip-wrapped tar, and zstd-wrapped tar
// archives. This guarantees both legacy (.tar.gz) and new (.tar / .tar.zst)
// archives produced by the engine restore correctly without filename hints.
func TestDetectingReader_AutoDetectsFormat(t *testing.T) {
	t.Parallel()

	const payload = "vault auto-detect roundtrip"

	makeTar := func(t *testing.T) []byte {
		t.Helper()
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(&tar.Header{Name: "f", Size: int64(len(payload)), Mode: 0o644}); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if _, err := tw.Write([]byte(payload)); err != nil {
			t.Fatalf("tar.Write: %v", err)
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("tar.Close: %v", err)
		}
		return buf.Bytes()
	}

	tarBytes := makeTar(t)

	var gzipped bytes.Buffer
	gw := gzip.NewWriter(&gzipped)
	if _, err := gw.Write(tarBytes); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	var zstded bytes.Buffer
	zw, err := zstd.NewWriter(&zstded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(tarBytes); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		data []byte
	}{
		{"plain", tarBytes},
		{"gzip", gzipped.Bytes()},
		{"zstd", zstded.Bytes()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, closeFn, err := detectingReader(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("detectingReader: %v", err)
			}
			defer closeFn()

			tr := tar.NewReader(r)
			hdr, err := tr.Next()
			if err != nil {
				t.Fatalf("tar.Next: %v", err)
			}
			if hdr.Name != "f" {
				t.Errorf("header name = %q, want f", hdr.Name)
			}
			var out strings.Builder
			if _, err := io.Copy(&out, tr); err != nil {
				t.Fatalf("io.Copy: %v", err)
			}
			if out.String() != payload {
				t.Errorf("payload = %q, want %q", out.String(), payload)
			}
		})
	}
}

// TestArchiveExt confirms the extension helper returns the user-visible
// filename suffix the engine appends after `.tar` for each compression mode.
func TestArchiveExt(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		CompressionNone: "",
		CompressionGzip: ".gz",
		CompressionZstd: ".zst",
		"":              "",
		"unknown":       "",
	}
	for in, want := range cases {
		if got := archiveExt(in); got != want {
			t.Errorf("archiveExt(%q) = %q, want %q", in, got, want)
		}
	}
}

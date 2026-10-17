package engine

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkContextCopySmallFiles measures the per-file cost of contextCopy on
// the archive path, where a share of many small files calls it once per file
// (issue #500).
func BenchmarkContextCopySmallFiles(b *testing.B) {
	const files, size = 256, 4 << 10
	data := bytes.Repeat([]byte{0x5a}, size)
	ctx := context.Background()
	b.ReportAllocs()
	b.SetBytes(files * size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for f := 0; f < files; f++ {
			if _, err := contextCopy(ctx, io.Discard, bytes.NewReader(data)); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkCopyFile measures a file-to-file restore copy, which reaches the
// kernel copy path when neither file is wrapped (issue #500).
func BenchmarkCopyFile(b *testing.B) {
	const size = 16 << 20
	dir := b.TempDir()
	src := filepath.Join(dir, "src.img")
	if err := os.WriteFile(src, bytes.Repeat([]byte{0xa5}, size), 0o644); err != nil {
		b.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.img")
	ctx := context.Background()
	b.ReportAllocs()
	b.SetBytes(size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := copyFile(ctx, src, dst); err != nil {
			b.Fatal(err)
		}
	}
}

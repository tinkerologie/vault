package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFromDefaultMatchesWrite(t *testing.T) {
	dir := t.TempDir()
	a := NewLocalAdapter(dir)
	data := []byte("write-from payload")
	err := a.WriteFrom("sub/obj.bin", func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	})
	if err != nil {
		t.Fatalf("WriteFrom error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "sub/obj.bin"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q, want %q", got, data)
	}
}

package dedup

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/storage"
)

// fakeAdapter is an in-memory storage.Adapter for testing. It supports
// Write / Read / ReadRange / Delete / List / Stat / TestConnection so we can
// use it interchangeably with the real LocalAdapter throughout the dedup
// package tests.
type fakeAdapter struct{ files map[string][]byte }

func newFakeAdapter() *fakeAdapter { return &fakeAdapter{files: map[string][]byte{}} }
func (f *fakeAdapter) Write(path string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.files[path] = b
	return nil
}
func (f *fakeAdapter) Read(path string) (io.ReadCloser, error) {
	b, ok := f.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (f *fakeAdapter) ReadRange(path string, offset, length int64) (io.ReadCloser, error) {
	b, ok := f.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	if offset >= int64(len(b)) {
		return nil, io.ErrUnexpectedEOF
	}
	end := offset + length
	if end > int64(len(b)) {
		end = int64(len(b))
	}
	return io.NopCloser(bytes.NewReader(b[offset:end])), nil
}
func (f *fakeAdapter) Delete(path string) error { delete(f.files, path); return nil }
func (f *fakeAdapter) List(prefix string) ([]storage.FileInfo, error) {
	out := []storage.FileInfo{}
	for k, v := range f.files {
		if strings.HasPrefix(k, prefix) {
			out = append(out, storage.FileInfo{Path: k, Size: int64(len(v))})
		}
	}
	return out, nil
}
func (f *fakeAdapter) Stat(path string) (storage.FileInfo, error) {
	b, ok := f.files[path]
	if !ok {
		return storage.FileInfo{}, errors.New("not found")
	}
	return storage.FileInfo{Path: path, Size: int64(len(b))}, nil
}
func (f *fakeAdapter) TestConnection() error { return nil }

func TestPackerFlushesAtTarget(t *testing.T) {
	a := newFakeAdapter()
	master := bytes.Repeat([]byte{0xaa}, 32)
	packsWritten := 0
	p := NewPacker(a, master, "test/packs", func(info PackInfo) { packsWritten++ })
	chunk := make([]byte, 1<<20) // 1 MiB
	for i := 0; i < 30; i++ {
		if _, err := rand.Read(chunk); err != nil {
			t.Fatal(err)
		}
		id := ChunkID(master, chunk)
		if _, err := p.Add(id, chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
	if packsWritten == 0 {
		t.Fatal("expected at least one pack written")
	}
}

func TestPackerFlushNoopOnEmpty(t *testing.T) {
	a := newFakeAdapter()
	master := bytes.Repeat([]byte{0xaa}, 32)
	p := NewPacker(a, master, "test/packs", func(info PackInfo) {
		t.Fatalf("onFlush should not be called for empty packer; got %+v", info)
	})
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestPackerFooterRoundTrip(t *testing.T) {
	a := newFakeAdapter()
	master := bytes.Repeat([]byte{0xab}, 32)
	var writtenPath string
	p := NewPacker(a, master, "test/packs", func(info PackInfo) { writtenPath = info.Path })
	chunks := [][]byte{
		[]byte("alpha"),
		[]byte("beta"),
		bytes.Repeat([]byte("gamma"), 100),
	}
	ids := make([]ID, len(chunks))
	for i, c := range chunks {
		ids[i] = ChunkID(master, c)
		if _, err := p.Add(ids[i], c); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
	if writtenPath == "" {
		t.Fatal("Flush did not write a pack")
	}
	entries, err := ReadPackFooter(a, writtenPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(chunks) {
		t.Fatalf("got %d footer entries, want %d", len(entries), len(chunks))
	}
	for i, e := range entries {
		if e.ID != ids[i] {
			t.Fatalf("entry %d id mismatch", i)
		}
	}
}

func TestPackerPathLayout(t *testing.T) {
	a := newFakeAdapter()
	master := bytes.Repeat([]byte{0xaa}, 32)
	var info PackInfo
	p := NewPacker(a, master, "_vault/packs", func(i PackInfo) { info = i })
	if _, err := p.Add(ChunkID(master, []byte("x")), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
	// Path should be "_vault/packs/<aa>/<32-byte-hex-id>"
	if !strings.HasPrefix(info.Path, "_vault/packs/") {
		t.Fatalf("path prefix mismatch: %q", info.Path)
	}
	parts := strings.Split(info.Path, "/")
	if len(parts) != 4 {
		t.Fatalf("expected 4 path parts, got %d (%q)", len(parts), info.Path)
	}
	if len(parts[3]) != 32 {
		t.Fatalf("pack ID should be 32 hex chars, got %d (%q)", len(parts[3]), parts[3])
	}
	if parts[2] != parts[3][:2] {
		t.Fatalf("shard dir %q should be first 2 chars of pack ID %q", parts[2], parts[3])
	}
}

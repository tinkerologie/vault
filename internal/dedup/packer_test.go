package dedup

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func TestPackerFlushesAtTarget(t *testing.T) {
	a := NewFakeAdapter()
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
	a := NewFakeAdapter()
	master := bytes.Repeat([]byte{0xaa}, 32)
	p := NewPacker(a, master, "test/packs", func(info PackInfo) {
		t.Fatalf("onFlush should not be called for empty packer; got %+v", info)
	})
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestPackerFooterRoundTrip(t *testing.T) {
	a := NewFakeAdapter()
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
	a := NewFakeAdapter()
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

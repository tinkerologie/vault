package dedup

import (
	"bytes"
	"crypto/rand"
	"io"
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

// TestAddRawRoundTrips proves a chunk written via Add and then copied
// verbatim into a new pack via AddRaw decrypts identically — the
// foundational guarantee compaction relies on (no decrypt/re-encrypt).
// The on-disk bytes are read back via ReadRange the same way Repo.Get does.
func TestAddRawRoundTrips(t *testing.T) {
	a := NewFakeAdapter()
	master := bytes.Repeat([]byte{0x11}, SecretSize)
	plain := make([]byte, 2048)
	_, _ = rand.Read(plain)
	id := ChunkID(DeriveChunkHashKey(master), plain)

	// 1) Write the chunk via the normal Add path.
	var firstInfo PackInfo
	p1 := NewPacker(a, master, "_vault/packs", func(info PackInfo) { firstInfo = info })
	if _, err := p1.Add(id, plain); err != nil {
		t.Fatal(err)
	}
	if err := p1.Flush(); err != nil {
		t.Fatal(err)
	}
	// 2) Read the chunk's raw on-disk bytes.
	entry := firstInfo.Entries[0]
	rc, err := a.ReadRange(firstInfo.Path, entry.Offset, entry.Length)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}

	// 3) AddRaw the bytes into a fresh pack.
	var secondInfo PackInfo
	p2 := NewPacker(a, master, "_vault/packs", func(info PackInfo) { secondInfo = info })
	got, err := p2.AddRaw(id, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id {
		t.Fatalf("AddRaw returned wrong ID: %x vs %x", got.ID, id)
	}
	if got.Length != int64(len(raw)) {
		t.Fatalf("AddRaw Length=%d, want %d", got.Length, len(raw))
	}
	if err := p2.Flush(); err != nil {
		t.Fatal(err)
	}

	// 4) Read the chunk back from the new pack and decrypt — must round-trip.
	e2 := secondInfo.Entries[0]
	rc, err = a.ReadRange(secondInfo.Path, e2.Offset, e2.Length)
	if err != nil {
		t.Fatal(err)
	}
	raw2, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw2) < 1 {
		t.Fatal("AddRaw output missing flags byte")
	}
	plain2, err := DecryptChunk(master, id, raw2[1:])
	if err != nil {
		t.Fatalf("decrypt after AddRaw: %v", err)
	}
	if !bytes.Equal(plain2, plain) {
		t.Fatal("AddRaw round-trip mismatch")
	}
}

func TestAddRawRejectsEmptyInput(t *testing.T) {
	a := NewFakeAdapter()
	master := bytes.Repeat([]byte{0x22}, SecretSize)
	p := NewPacker(a, master, "_vault/packs", func(info PackInfo) {})
	if _, err := p.AddRaw(ID{}, nil); err == nil {
		t.Fatal("AddRaw should reject empty input")
	}
	// A valid on-disk chunk is flags(1) + ciphertext(>=0) + AEAD-tag(16),
	// so the floor is 17 bytes. 16 bytes is too short and must be rejected
	// up front rather than landing in the pack and producing a
	// "message authentication failed" error at read time.
	if _, err := p.AddRaw(ID{}, make([]byte, 16)); err == nil {
		t.Fatal("AddRaw should reject sub-minimum (16-byte) input — needs >=17 bytes")
	}
}

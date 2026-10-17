package dedup

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/tinkerologie/vault/internal/storage"
)

// discardAdapter consumes every Write synchronously and counts the bytes,
// so pack benchmarks measure the packer rather than storage.
type discardAdapter struct {
	FakeAdapter
	written int64
}

func (d *discardAdapter) Write(_ string, r io.Reader) error {
	n, err := io.Copy(io.Discard, r)
	d.written += n
	return err
}

var _ storage.Adapter = (*discardAdapter)(nil)

// benchChunk returns a unique, incompressible-looking chunk for index i.
func benchChunk(size, i int) []byte {
	b := make([]byte, size)
	for off := 0; off+8 <= size; off += 8 {
		binary.LittleEndian.PutUint64(b[off:], uint64(i)*0x9e3779b97f4a7c15+uint64(off))
	}
	return b
}

func benchID(i int) ID {
	var id ID
	binary.LittleEndian.PutUint64(id[:], uint64(i))
	return id
}

var benchMaster = bytes.Repeat([]byte{0x42}, 32)

func BenchmarkEncryptChunk(b *testing.B) {
	plain := benchChunk(ChunkAvg, 1)
	b.SetBytes(int64(len(plain)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncryptChunk(benchMaster, benchID(i), plain); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecryptChunk(b *testing.B) {
	plain := benchChunk(ChunkAvg, 1)
	id := benchID(1)
	ct, err := EncryptChunk(benchMaster, id, plain)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(plain)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecryptChunk(benchMaster, id, ct); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPackerAdd measures Add including the flushes it triggers every
// PackTargetSize bytes.
func BenchmarkPackerAdd(b *testing.B) {
	chunks := make([][]byte, 64)
	for i := range chunks {
		chunks[i] = benchChunk(ChunkAvg, i)
	}
	a := &discardAdapter{FakeAdapter: *NewFakeAdapter()}
	p := NewPacker(a, benchMaster, "data", nil)
	b.SetBytes(ChunkAvg)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.Add(benchID(i), chunks[i%len(chunks)]); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if err := p.Flush(); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkPackFlush measures one full pack: PackTargetSize of chunks added
// and flushed.
func BenchmarkPackFlush(b *testing.B) {
	perPack := PackTargetSize / ChunkAvg
	chunks := make([][]byte, perPack)
	for i := range chunks {
		chunks[i] = benchChunk(ChunkAvg, i)
	}
	a := &discardAdapter{FakeAdapter: *NewFakeAdapter()}
	p := NewPacker(a, benchMaster, "data", nil)
	b.SetBytes(int64(perPack * ChunkAvg))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < perPack-1; j++ {
			if _, err := p.Add(benchID(i*perPack+j), chunks[j]); err != nil {
				b.Fatal(err)
			}
		}
		if err := p.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRepoGet(b *testing.B) {
	r, _, cleanup := newTestRepo(b)
	defer cleanup()
	ids := make([]ID, 32)
	for i := range ids {
		id, err := r.Put(benchChunk(ChunkAvg, i))
		if err != nil {
			b.Fatal(err)
		}
		ids[i] = id
	}
	if err := r.Flush(); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(ChunkAvg)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Get(ids[i%len(ids)]); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkChunkerSplitSmallFile is one small file through Split, the
// per-file cost of a deduplicated folder backup (issue #500).
func BenchmarkChunkerSplitSmallFile(b *testing.B) {
	c, err := NewChunker(make([]byte, SecretSize))
	if err != nil {
		b.Fatal(err)
	}
	data := bytes.Repeat([]byte{0x5a}, 16<<10)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if err := c.Split(bytes.NewReader(data), func([]byte) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}

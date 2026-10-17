package dedup

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

// These tests pin the ownership contracts behind the allocation work in
// #500: pooled read scratch, sealing into the pack buffer, and flushing the
// pack buffer in place.

func TestAppendEncryptChunkMatchesEncryptChunk(t *testing.T) {
	master := bytes.Repeat([]byte{0x11}, 32)
	id := benchID(7)
	plain := benchChunk(4096, 7)

	want, err := EncryptChunk(master, id, plain)
	if err != nil {
		t.Fatal(err)
	}
	prefix := []byte("prefix")
	dst := append(make([]byte, 0, 64), prefix...)
	got, err := appendEncryptChunk(dst, master, id, plain)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:len(prefix)], prefix) {
		t.Fatal("appendEncryptChunk clobbered the dst prefix")
	}
	if !bytes.Equal(got[len(prefix):], want) {
		t.Fatal("appended ciphertext differs from EncryptChunk")
	}
	if len(want) != len(plain)+chunkTagSize {
		t.Fatalf("ciphertext len %d, want plaintext+%d", len(want), chunkTagSize)
	}
}

func TestGetResultSurvivesScratchReuse(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	var ids []ID
	var plains [][]byte
	for i := range 8 {
		p := benchChunk(ChunkMin+i*1000, i)
		id, err := r.Put(p)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		plains = append(plains, p)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	first, err := r.Get(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		for _, id := range ids[1:] {
			if _, err := r.Get(id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !bytes.Equal(first, plains[0]) {
		t.Fatal("Get result changed after later reads reused the scratch buffer")
	}
}

func TestStoredDataIndependentOfCallerBuffers(t *testing.T) {
	master := bytes.Repeat([]byte{0x22}, 32)
	a := NewFakeAdapter()
	var info PackInfo
	p := NewPacker(a, master, "data", func(pi PackInfo) { info = pi })

	plain := benchChunk(2048, 1)
	orig := append([]byte(nil), plain...)
	id := benchID(1)
	if _, err := p.Add(id, plain); err != nil {
		t.Fatal(err)
	}
	clear(plain)

	raw, err := EncryptChunk(master, benchID(2), benchChunk(1024, 2))
	if err != nil {
		t.Fatal(err)
	}
	rawRec := append([]byte{0}, raw...)
	wantRaw := append([]byte(nil), rawRec...)
	if _, err := p.AddRaw(benchID(2), rawRec); err != nil {
		t.Fatal(err)
	}
	clear(rawRec)

	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
	pack := a.files[info.Path]
	e0, e1 := info.Entries[0], info.Entries[1]
	got, err := DecryptChunk(master, id, pack[e0.Offset+1:e0.Offset+e0.Length])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, orig) {
		t.Fatal("Add stored data changed when the caller reused its buffer")
	}
	if !bytes.Equal(pack[e1.Offset:e1.Offset+e1.Length], wantRaw) {
		t.Fatal("AddRaw stored data changed when the caller reused its buffer")
	}
}

func TestFlushEntrySnapshotsSurviveLaterFlushes(t *testing.T) {
	master := bytes.Repeat([]byte{0x33}, 32)
	var infos []PackInfo
	p := NewPacker(NewFakeAdapter(), master, "data", func(pi PackInfo) { infos = append(infos, pi) })
	for pack := range 3 {
		for j := range 3 {
			if _, err := p.Add(benchID(pack*10+j), benchChunk(512, pack*10+j)); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	for pack, info := range infos {
		if len(info.Entries) != 3 {
			t.Fatalf("pack %d: %d entries, want 3", pack, len(info.Entries))
		}
		for j, e := range info.Entries {
			if e.ID != benchID(pack*10+j) {
				t.Fatalf("pack %d entry %d was overwritten by a later flush", pack, j)
			}
		}
	}
}

// failOnceAdapter fails the first pack Write after draining part of the
// reader, then behaves like FakeAdapter.
type failOnceAdapter struct {
	*FakeAdapter
	failed bool
}

func (f *failOnceAdapter) Write(path string, r io.Reader) error {
	if !f.failed {
		f.failed = true
		_, _ = io.CopyN(io.Discard, r, 100)
		return errors.New("injected write failure")
	}
	return f.FakeAdapter.Write(path, r)
}

func TestFlushRetryAfterWriteFailureKeepsPayload(t *testing.T) {
	master := bytes.Repeat([]byte{0x44}, 32)

	// Reference pack from a packer whose first write succeeds.
	refAdapter := NewFakeAdapter()
	var ref PackInfo
	refPacker := NewPacker(refAdapter, master, "data", func(pi PackInfo) { ref = pi })

	fa := &failOnceAdapter{FakeAdapter: NewFakeAdapter()}
	var got PackInfo
	p := NewPacker(fa, master, "data", func(pi PackInfo) { got = pi })

	for i := range 4 {
		for _, pk := range []*Packer{refPacker, p} {
			if _, err := pk.Add(benchID(i), benchChunk(1500, i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := refPacker.Flush(); err != nil {
		t.Fatal(err)
	}
	pendingBefore := p.buf.Len()
	if err := p.Flush(); err == nil {
		t.Fatal("first Flush should fail")
	}
	if p.buf.Len() != pendingBefore {
		t.Fatalf("failed Flush left %d buffered bytes, want %d", p.buf.Len(), pendingBefore)
	}
	if err := p.Flush(); err != nil {
		t.Fatalf("retry Flush: %v", err)
	}
	refBody, body := refAdapter.files[ref.Path], fa.files[got.Path]
	if !bytes.Equal(refBody, body) {
		t.Fatal("retried pack differs from a pack flushed first time")
	}
	if got.SizeBytes != int64(len(body)) {
		t.Fatalf("PackInfo.SizeBytes %d, body %d", got.SizeBytes, len(body))
	}
	entries, err := ReadPackFooter(fa, got.Path)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		pt, err := DecryptChunk(master, e.ID, body[e.Offset+1:e.Offset+e.Length])
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		if !bytes.Equal(pt, benchChunk(1500, i)) {
			t.Fatalf("entry %d plaintext mismatch", i)
		}
	}
}

func TestGetRejectsDamagedChunks(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	id, err := r.Put(benchChunk(ChunkMin, 9))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	packPath, offset, length, err := r.idx.Locate(id)
	if err != nil {
		t.Fatal(err)
	}
	fa := r.adapter.(*FakeAdapter)
	orig := append([]byte(nil), fa.files[packPath]...)
	restore := func() { fa.files[packPath] = append([]byte(nil), orig...) }

	cases := []struct {
		name   string
		damage func(pack []byte) []byte
	}{
		{"tampered", func(p []byte) []byte { p[offset+length/2] ^= 0xff; return p }},
		{"truncated", func(p []byte) []byte { return p[:offset+length/2] }},
		{"flag only", func(p []byte) []byte { return p[:offset+1] }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer restore()
			fa.files[packPath] = c.damage(append([]byte(nil), orig...))
			pt, err := r.Get(id)
			if err == nil {
				t.Fatal("Get accepted a damaged chunk")
			}
			if pt != nil {
				t.Fatal("Get returned plaintext alongside an error")
			}
		})
	}

}

// errAfterReader returns its bytes and then a non-EOF error.
type errAfterReader struct {
	data []byte
	err  error
}

func (e *errAfterReader) Read(p []byte) (int, error) {
	if len(e.data) == 0 {
		return 0, e.err
	}
	n := copy(p, e.data)
	e.data = e.data[n:]
	return n, nil
}

func TestGetPooledReadErrorAndEmpty(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	if _, err := r.getPooled(benchID(1), &errAfterReader{data: []byte{0, 1}, err: errors.New("boom")}, 64); err == nil || !bytes.Contains([]byte(err.Error()), []byte("read chunk body")) {
		t.Fatalf("want read error, got %v", err)
	}
	if _, err := r.getPooled(benchID(1), bytes.NewReader(nil), 64); err == nil || !bytes.Contains([]byte(err.Error()), []byte("missing flags byte")) {
		t.Fatalf("want too-short error, got %v", err)
	}
}

// TestGetLargeChunkUsesReadAllPath covers records bigger than the pooled
// scratch (a Put of up to PackTargetSize): they keep the io.ReadAll path and
// still round-trip.
func TestGetLargeChunkUsesReadAllPath(t *testing.T) {
	r, _, cleanup := newTestRepo(t)
	defer cleanup()
	big := make([]byte, chunkScratchSize+1<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	id, err := r.Put(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, big) {
		t.Fatal("large chunk did not round-trip")
	}
}

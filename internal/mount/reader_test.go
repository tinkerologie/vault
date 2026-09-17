package mount

import (
	"bytes"
	"crypto/rand"
	"io"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
)

func newTestDedupRepo(t *testing.T) (*dedup.Repo, func()) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name:         "test-dest",
		Type:         "local",
		Config:       "{}",
		DedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	serverKey := bytes.Repeat([]byte{0xab}, dedup.SecretSize)
	adapter := dedup.NewFakeAdapter()
	repo, err := dedup.InitRepo(d, adapter, destID, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	return repo, func() { _ = d.Close() }
}

func TestChunkCache_LRU(t *testing.T) {
	cache := NewChunkCache(100) // 100 bytes max

	id1 := dedup.ID{1}
	id2 := dedup.ID{2}
	id3 := dedup.ID{3}

	data1 := bytes.Repeat([]byte("a"), 40)
	data2 := bytes.Repeat([]byte("b"), 40)
	data3 := bytes.Repeat([]byte("c"), 40)

	cache.Put(id1, data1)
	cache.Put(id2, data2)

	// Verify both exist
	if _, ok := cache.Get(id2); !ok {
		t.Errorf("expected id2 in cache")
	}
	if _, ok := cache.Get(id1); !ok {
		t.Errorf("expected id1 in cache")
	}

	// Putting id3 (40 bytes) exceeds 100 bytes total (40+40+40 = 120 > 100).
	// Since id1 was accessed after id2, id2 is LRU and must be evicted.
	cache.Put(id3, data3)

	if _, ok := cache.Get(id2); ok {
		t.Errorf("expected id2 to be evicted")
	}
	if _, ok := cache.Get(id1); !ok {
		t.Errorf("expected id1 to still be present")
	}
	if _, ok := cache.Get(id3); !ok {
		t.Errorf("expected id3 to be present")
	}
}

func TestFileReader_EmptyFile(t *testing.T) {
	repo, cleanup := newTestDedupRepo(t)
	defer cleanup()

	entry := dedup.ManifestEntry{
		Size: 0,
		Mode: 0644,
	}

	r, err := NewFileReader(repo, entry, nil)
	if err != nil {
		t.Fatalf("NewFileReader failed: %v", err)
	}

	buf := make([]byte, 10)
	n, err := r.ReadAt(buf, 0)
	if err != io.EOF {
		t.Errorf("expected io.EOF on empty file, got: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes read, got %d", n)
	}
}

func TestFileReader_SingleChunk(t *testing.T) {
	repo, cleanup := newTestDedupRepo(t)
	defer cleanup()

	content := []byte("Hello, world! This is a test chunk for Vault FUSE reader.")
	chunkID, err := repo.Put(content)
	if err != nil {
		t.Fatalf("repo.Put failed: %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatalf("repo.Flush failed: %v", err)
	}

	entry := dedup.ManifestEntry{
		Size:   int64(len(content)),
		Mode:   0644,
		Chunks: []dedup.ID{chunkID},
	}

	r, err := NewFileReader(repo, entry, nil)
	if err != nil {
		t.Fatalf("NewFileReader failed: %v", err)
	}

	// Read full content
	full := make([]byte, len(content))
	n, err := r.ReadAt(full, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt full failed: %v", err)
	}
	if n != len(content) || !bytes.Equal(full, content) {
		t.Errorf("full content mismatch: got %q want %q", full, content)
	}

	// Read slice at offset
	part := make([]byte, 5)
	n, err = r.ReadAt(part, 7) // "world"
	if err != nil {
		t.Fatalf("ReadAt offset failed: %v", err)
	}
	if n != 5 || string(part) != "world" {
		t.Errorf("offset read mismatch: got %q want 'world'", string(part))
	}

	// Read past EOF
	past := make([]byte, 10)
	n, err = r.ReadAt(past, int64(len(content)+5))
	if err != io.EOF {
		t.Errorf("expected io.EOF reading past end, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes, got %d", n)
	}
}

func TestFileReader_MultiChunkSpanning(t *testing.T) {
	repo, cleanup := newTestDedupRepo(t)
	defer cleanup()

	part1 := make([]byte, 1024)
	part2 := make([]byte, 2048)
	part3 := make([]byte, 512)

	_, _ = rand.Read(part1)
	_, _ = rand.Read(part2)
	_, _ = rand.Read(part3)

	fullContent := append(append(append([]byte(nil), part1...), part2...), part3...)

	c1, err := repo.Put(part1)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := repo.Put(part2)
	if err != nil {
		t.Fatal(err)
	}
	c3, err := repo.Put(part3)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	entry := dedup.ManifestEntry{
		Size:   int64(len(fullContent)),
		Mode:   0644,
		Chunks: []dedup.ID{c1, c2, c3},
	}

	r, err := NewFileReader(repo, entry, NewChunkCache(10*1024*1024))
	if err != nil {
		t.Fatalf("NewFileReader failed: %v", err)
	}

	// Test 1: Read entire multi-chunk file
	readAll := make([]byte, len(fullContent))
	n, err := r.ReadAt(readAll, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt entire failed: %v", err)
	}
	if n != len(fullContent) || !bytes.Equal(readAll, fullContent) {
		t.Errorf("ReadAt entire mismatch (read %d of %d)", n, len(fullContent))
	}

	// Test 2: Span across boundary of chunk 1 and chunk 2
	// offset: 1000 (last 24 bytes of part1, first 100 bytes of part2)
	spanLen := 124
	spanBuf := make([]byte, spanLen)
	n, err = r.ReadAt(spanBuf, 1000)
	if err != nil {
		t.Fatalf("ReadAt span failed: %v", err)
	}
	expectedSpan := fullContent[1000 : 1000+spanLen]
	if n != spanLen || !bytes.Equal(spanBuf, expectedSpan) {
		t.Errorf("ReadAt span mismatch")
	}

	// Test 3: Read across all three chunks
	tripleLen := 2500
	tripleBuf := make([]byte, tripleLen)
	n, err = r.ReadAt(tripleBuf, 500)
	if err != nil {
		t.Fatalf("ReadAt triple failed: %v", err)
	}
	expectedTriple := fullContent[500 : 500+tripleLen]
	if n != tripleLen || !bytes.Equal(tripleBuf, expectedTriple) {
		t.Errorf("ReadAt triple mismatch")
	}
}

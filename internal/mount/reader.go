package mount

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// DefaultCacheSizeBytes is the default maximum memory limit for the chunk cache (64 MiB).
const DefaultCacheSizeBytes = 64 * 1024 * 1024

type cacheNode struct {
	id   dedup.ID
	data []byte
	prev *cacheNode
	next *cacheNode
}

// ChunkCache is a thread-safe LRU cache for decrypted deduplication chunks.
type ChunkCache struct {
	mu        sync.Mutex
	maxBytes  int64
	currBytes int64
	nodes     map[dedup.ID]*cacheNode
	head      *cacheNode // MRU
	tail      *cacheNode // LRU
}

// NewChunkCache creates a new ChunkCache bounded by maxBytes.
func NewChunkCache(maxBytes int64) *ChunkCache {
	if maxBytes <= 0 {
		maxBytes = DefaultCacheSizeBytes
	}
	return &ChunkCache{
		maxBytes: maxBytes,
		nodes:    make(map[dedup.ID]*cacheNode),
	}
}

// Get returns the chunk data from cache if present.
func (c *ChunkCache) Get(id dedup.ID) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	node, ok := c.nodes[id]
	if !ok {
		return nil, false
	}
	c.moveToHead(node)
	return node.data, true
}

// Put adds a chunk to the cache, evicting the least recently used chunks if needed.
func (c *ChunkCache) Put(id dedup.ID, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if node, ok := c.nodes[id]; ok {
		c.currBytes += int64(len(data) - len(node.data))
		node.data = data
		c.moveToHead(node)
		c.evict()
		return
	}

	node := &cacheNode{
		id:   id,
		data: data,
	}
	c.nodes[id] = node
	c.addToHead(node)
	c.currBytes += int64(len(data))
	c.evict()
}

func (c *ChunkCache) addToHead(node *cacheNode) {
	node.next = c.head
	node.prev = nil
	if c.head != nil {
		c.head.prev = node
	}
	c.head = node
	if c.tail == nil {
		c.tail = node
	}
}

func (c *ChunkCache) removeNode(node *cacheNode) {
	if node.prev != nil {
		node.prev.next = node.next
	} else {
		c.head = node.next
	}
	if node.next != nil {
		node.next.prev = node.prev
	} else {
		c.tail = node.prev
	}
	node.prev = nil
	node.next = nil
}

func (c *ChunkCache) moveToHead(node *cacheNode) {
	if c.head == node {
		return
	}
	c.removeNode(node)
	c.addToHead(node)
}

func (c *ChunkCache) evict() {
	for c.currBytes > c.maxBytes && c.tail != nil {
		oldest := c.tail
		c.removeNode(oldest)
		delete(c.nodes, oldest.id)
		c.currBytes -= int64(len(oldest.data))
	}
}

type chunkSpan struct {
	id    dedup.ID
	start int64
	end   int64
}

// FileReader provides random-access byte-level reading (io.ReaderAt) over a dedup.ManifestEntry.
type FileReader struct {
	repo  *dedup.Repo
	entry dedup.ManifestEntry
	spans []chunkSpan
	cache *ChunkCache
}

// NewFileReader constructs a FileReader for a regular file entry.
func NewFileReader(repo *dedup.Repo, entry dedup.ManifestEntry, cache *ChunkCache) (*FileReader, error) {
	if repo == nil {
		return nil, errors.New("mount: nil dedup repo")
	}
	if cache == nil {
		cache = NewChunkCache(DefaultCacheSizeBytes)
	}

	fr := &FileReader{
		repo:  repo,
		entry: entry,
		cache: cache,
	}

	if entry.Size <= 0 {
		return fr, nil
	}
	if len(entry.Chunks) == 0 {
		return nil, fmt.Errorf("mount: corrupt manifest entry: size %d but 0 chunks", entry.Size)
	}

	if len(entry.Chunks) == 1 {
		fr.spans = []chunkSpan{
			{
				id:    entry.Chunks[0],
				start: 0,
				end:   entry.Size,
			},
		}
		return fr, nil
	}

	// For multi-chunk files, calculate cumulative offsets
	spans := make([]chunkSpan, 0, len(entry.Chunks))
	offset := int64(0)

	for i, chunkID := range entry.Chunks {
		var chunkSize int64
		// If it's the last chunk, calculate remaining bytes directly
		if i == len(entry.Chunks)-1 {
			chunkSize = entry.Size - offset
		} else {
			// Locate in index: pack chunk length is 1-byte flag + ciphertext + 16-byte AEAD tag
			_, _, length, err := repo.LocateForVerify(chunkID)
			if err == nil && length > 17 {
				chunkSize = length - 17
			} else {
				// Fallback to reading and caching chunk
				data, err := fr.fetchChunk(chunkID)
				if err != nil {
					return nil, fmt.Errorf("mount: resolve chunk %x: %w", chunkID[:8], err)
				}
				chunkSize = int64(len(data))
			}
		}

		spans = append(spans, chunkSpan{
			id:    chunkID,
			start: offset,
			end:   offset + chunkSize,
		})
		offset += chunkSize
	}

	fr.spans = spans
	return fr, nil
}

func (r *FileReader) fetchChunk(id dedup.ID) ([]byte, error) {
	if data, ok := r.cache.Get(id); ok {
		return data, nil
	}
	data, err := r.repo.Get(id)
	if err != nil {
		return nil, err
	}
	r.cache.Put(id, data)
	return data, nil
}

// Entry returns the underlying ManifestEntry.
func (r *FileReader) Entry() dedup.ManifestEntry {
	return r.entry
}

// Size returns the file size in bytes.
func (r *FileReader) Size() int64 {
	return r.entry.Size
}

// ReadAt reads len(p) bytes into p starting at offset off in the file.
func (r *FileReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("mount: negative offset")
	}
	if off >= r.entry.Size {
		return 0, io.EOF
	}

	toRead := int64(len(p))
	if off+toRead > r.entry.Size {
		toRead = r.entry.Size - off
	}

	totalRead := 0

	for _, span := range r.spans {
		if span.end <= off {
			continue
		}
		if span.start >= off+toRead {
			break
		}

		chunkData, err := r.fetchChunk(span.id)
		if err != nil {
			return totalRead, fmt.Errorf("mount: fetch chunk %x: %w", span.id[:8], err)
		}

		expectedSpanLen := span.end - span.start
		if int64(len(chunkData)) != expectedSpanLen {
			return totalRead, fmt.Errorf("mount: chunk length mismatch: got %d bytes, want %d", len(chunkData), expectedSpanLen)
		}

		// Calculate overlap between [off, off+toRead) and [span.start, span.end)
		chunkStartOffset := int64(0)
		if off > span.start {
			chunkStartOffset = off - span.start
		}

		chunkEndOffset := int64(len(chunkData))
		if off+toRead < span.end {
			chunkEndOffset = off + toRead - span.start
		}

		if chunkStartOffset > int64(len(chunkData)) {
			chunkStartOffset = int64(len(chunkData))
		}
		if chunkEndOffset > int64(len(chunkData)) {
			chunkEndOffset = int64(len(chunkData))
		}

		slice := chunkData[chunkStartOffset:chunkEndOffset]
		copied := copy(p[totalRead:], slice)
		totalRead += copied

		if int64(totalRead) >= toRead {
			break
		}
	}

	if totalRead < len(p) {
		if off+int64(totalRead) >= r.entry.Size {
			return totalRead, io.EOF
		}
		return totalRead, io.ErrUnexpectedEOF
	}

	return totalRead, nil
}

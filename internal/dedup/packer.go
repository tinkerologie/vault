package dedup

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"

	"github.com/ruaan-deysel/vault/internal/storage"
)

// PackTargetSize is the buffer threshold at which the packer flushes a pack.
// 24 MiB matches Kopia's default — small enough for adapters to retry on
// transient failure, large enough that S3 PUT amortises well.
const PackTargetSize = 24 * 1024 * 1024

// PackVersion is the on-disk pack format version. Bump on incompatible
// changes; ReadPackFooter checks this and refuses unknown versions.
const PackVersion = 1

// PackInfo describes a pack that has just been flushed to storage. The
// per-pack onFlush callback (registered with NewPacker) receives one per
// flush so the index layer can persist the chunk-to-pack mapping.
type PackInfo struct {
	ID         string
	Path       string
	SizeBytes  int64
	ChunkCount int
	Entries    []PackEntry
}

// PackEntry is one chunk's location within a pack file. Length is the
// on-disk length (1 flag byte + ciphertext + 16-byte AEAD tag) — what
// ReadRange should ask for to retrieve this chunk.
type PackEntry struct {
	ID     ID    `json:"id"`
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
	Flags  byte  `json:"flags"`
}

type packFooter struct {
	Version int         `json:"version"`
	Chunks  []PackEntry `json:"chunks"`
}

// Packer accumulates encrypted chunks and flushes them as pack blobs.
//
// Not safe for concurrent Add — callers serialise access (typical pattern is
// one Packer per Repo, accessed under the Repo's per-destination mutex).
type Packer struct {
	adapter  storage.Adapter
	master   []byte
	rootPath string
	onFlush  func(PackInfo)

	buf     bytes.Buffer
	entries []PackEntry
}

// NewPacker constructs a packer. onFlush is invoked synchronously inside
// Add() / Flush() when a pack is uploaded.
func NewPacker(a storage.Adapter, master []byte, rootPath string, onFlush func(PackInfo)) *Packer {
	return &Packer{adapter: a, master: master, rootPath: rootPath, onFlush: onFlush}
}

// Add encrypts plaintext under chunkID and appends to the in-memory pack.
// Returns the PackEntry the chunk will occupy after Flush; the caller uses
// this for the SQLite index in Task 5. If adding this chunk pushes the
// buffer past PackTargetSize, the pack is flushed before returning.
func (p *Packer) Add(chunkID ID, plaintext []byte) (PackEntry, error) {
	ct, err := EncryptChunk(p.master, chunkID, plaintext)
	if err != nil {
		return PackEntry{}, err
	}
	// flags byte: bit0 = compressed (reserved; always 0 in v1).
	var flags byte = 0
	offset := int64(p.buf.Len())
	onDiskLen := int64(1 + len(ct))
	p.buf.WriteByte(flags)
	p.buf.Write(ct)
	e := PackEntry{ID: chunkID, Offset: offset, Length: onDiskLen, Flags: flags}
	p.entries = append(p.entries, e)
	if p.buf.Len() >= PackTargetSize {
		if err := p.Flush(); err != nil {
			return PackEntry{}, err
		}
	}
	return e, nil
}

// Flush uploads any pending pack. No-op if the buffer is empty.
func (p *Packer) Flush() error {
	if p.buf.Len() == 0 {
		return nil
	}
	packID, err := randomHex(16)
	if err != nil {
		return err
	}
	packPath := path.Join(p.rootPath, packID[:2], packID)

	footer, err := json.Marshal(packFooter{Version: PackVersion, Chunks: p.entries})
	if err != nil {
		return err
	}

	body := bytes.NewBuffer(make([]byte, 0, p.buf.Len()+len(footer)+4))
	body.Write(p.buf.Bytes())
	body.Write(footer)
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(footer)))
	body.Write(lenBuf)

	size := int64(body.Len())
	if err := p.adapter.Write(packPath, body); err != nil {
		return fmt.Errorf("dedup: write pack %s: %w", packPath, err)
	}

	info := PackInfo{
		ID:         packID,
		Path:       packPath,
		SizeBytes:  size,
		ChunkCount: len(p.entries),
		Entries:    append([]PackEntry(nil), p.entries...),
	}
	if p.onFlush != nil {
		p.onFlush(info)
	}

	p.buf.Reset()
	p.entries = p.entries[:0]
	return nil
}

// ReadPackFooter reads a pack via the adapter's ReadRange and parses its
// footer. The pack is self-describing — we don't need the SQLite index to
// know what chunks it contains. Used by the disaster-recovery rebuild path
// (Task 14 CLI: `vault dedup repair`).
func ReadPackFooter(a storage.Adapter, packPath string) ([]PackEntry, error) {
	info, err := a.Stat(packPath)
	if err != nil {
		return nil, fmt.Errorf("dedup: stat pack %s: %w", packPath, err)
	}
	if info.Size < 4 {
		return nil, fmt.Errorf("dedup: pack %s too small (%d bytes)", packPath, info.Size)
	}
	rc, err := a.ReadRange(packPath, info.Size-4, 4)
	if err != nil {
		return nil, fmt.Errorf("dedup: read footer-length: %w", err)
	}
	lenBuf := make([]byte, 4)
	_, _ = io.ReadFull(rc, lenBuf)
	_ = rc.Close()
	footerLen := int64(binary.BigEndian.Uint32(lenBuf))
	if footerLen <= 0 || footerLen+4 > info.Size {
		return nil, fmt.Errorf("dedup: invalid footer length %d in pack %s", footerLen, packPath)
	}
	rc, err = a.ReadRange(packPath, info.Size-4-footerLen, footerLen)
	if err != nil {
		return nil, fmt.Errorf("dedup: read footer: %w", err)
	}
	defer rc.Close()
	var f packFooter
	if err := json.NewDecoder(rc).Decode(&f); err != nil {
		return nil, fmt.Errorf("dedup: decode footer: %w", err)
	}
	if f.Version != PackVersion {
		return nil, fmt.Errorf("dedup: unsupported pack version %d", f.Version)
	}
	return f.Chunks, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

package dedup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/hkdf"
)

// ID is a 32-byte content identifier produced by HMAC-BLAKE2b-256(hashKey, plaintext).
type ID [32]byte

// ChunkID computes the deterministic content ID for a chunk. hashKey is
// typically 32 bytes (callers derive it from the per-destination master via
// DeriveChunkHashKey). Plaintext is hashed BEFORE compression / encryption,
// so identical plaintext from any source produces the same chunkID inside
// one destination.
func ChunkID(hashKey, plaintext []byte) ID {
	mac, err := blake2b.New256(hashKey)
	if err != nil {
		// blake2b.New256 only errors on invalid key length (>64 bytes), which
		// would be a programmer error at the call site.
		panic(fmt.Errorf("dedup: blake2b new256: %w", err))
	}
	_, _ = mac.Write(plaintext)
	var out ID
	copy(out[:], mac.Sum(nil))
	return out
}

// DeriveChunkHashKey returns the HMAC-BLAKE2b key for chunk IDs in this destination.
func DeriveChunkHashKey(master []byte) []byte {
	return deriveKey(master, "vault/chunk-hash/v1", 32)
}

// DeriveSplitterSecret returns the secret seed for the KFastCDC Gear table.
// Pass this directly to dedup.NewChunker() — different from chunk-hash key
// (different HKDF info string) so a leak of one does not weaken the other.
func DeriveSplitterSecret(master []byte) []byte {
	return deriveKey(master, "vault/splitter/v1", SecretSize)
}

// deriveChunkEncKey returns the per-chunk AES-256-GCM key for chunkID.
// Per-chunk derivation means we can safely use a zero nonce in EncryptChunk —
// the key is used exactly once across all chunks in this destination.
func deriveChunkEncKey(master []byte, chunkID ID) []byte {
	info := append([]byte("vault/chunk-enc/v1"), chunkID[:]...)
	return deriveKey(master, string(info), 32)
}

// deriveKey runs HKDF-SHA256 over master with the given info string and
// returns n bytes of derived key material. info MUST be domain-separated
// (e.g. include a version suffix) to keep sub-keys independent.
func deriveKey(master []byte, info string, n int) []byte {
	r := hkdf.New(sha256.New, master, nil, []byte(info))
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		// HKDF's reader only errors when exceeding 255*HashSize output bytes,
		// which we never do with n<=64. Treat as programmer error.
		panic(fmt.Errorf("dedup: hkdf read: %w", err))
	}
	return out
}

// EncryptChunk encrypts plaintext under a per-chunk key derived from master +
// chunkID. Returns ciphertext || tag. Nonce is fixed-zero; safe because the
// per-chunk key is only ever used for this exact chunkID.
//
// chunkID is also bound into the AEAD's additional-data so a tamperer can't
// swap ciphertexts between chunks even if they could re-key.
func EncryptChunk(master []byte, chunkID ID, plaintext []byte) ([]byte, error) {
	key := deriveChunkEncKey(master, chunkID)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("dedup: aes new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("dedup: gcm new: %w", err)
	}
	nonce := make([]byte, aead.NonceSize()) // 12 zero bytes — safe because of per-chunk key
	// #nosec G407 -- per-chunk AES-GCM key is HKDF-derived from master ‖ chunkID, so the (key, nonce) pair is unique per chunk; nonce-reuse-with-same-key is impossible by construction.
	return aead.Seal(nil, nonce, plaintext, chunkID[:]), nil
}

// DecryptChunk inverts EncryptChunk. Authentication failure (wrong key,
// tampered ciphertext, or mismatched chunkID) returns an error and never
// returns garbage plaintext.
func DecryptChunk(master []byte, chunkID ID, ciphertext []byte) ([]byte, error) {
	key := deriveChunkEncKey(master, chunkID)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("dedup: aes new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("dedup: gcm new: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	out, err := aead.Open(nil, nonce, ciphertext, chunkID[:])
	if err != nil {
		return nil, fmt.Errorf("dedup: gcm open: %w", err)
	}
	return out, nil
}

// SealMaster wraps repoMaster with the daemon's serverKey using AES-256-GCM
// and a fresh random nonce. Output layout: nonce || ciphertext || tag.
// The only secret persisted on a destination is the output of this function.
func SealMaster(serverKey, repoMaster []byte) ([]byte, error) {
	if len(serverKey) != SecretSize {
		return nil, fmt.Errorf("dedup: serverKey must be %d bytes, got %d", SecretSize, len(serverKey))
	}
	if len(repoMaster) != SecretSize {
		return nil, fmt.Errorf("dedup: repoMaster must be %d bytes, got %d", SecretSize, len(repoMaster))
	}
	block, err := aes.NewCipher(serverKey)
	if err != nil {
		return nil, fmt.Errorf("dedup: aes new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("dedup: gcm new: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("dedup: rand read nonce: %w", err)
	}
	ct := aead.Seal(nil, nonce, repoMaster, nil)
	return append(nonce, ct...), nil
}

// UnsealMaster inverts SealMaster. Wrong serverKey or tampered envelope
// returns an error (never returns garbage plaintext).
func UnsealMaster(serverKey, sealed []byte) ([]byte, error) {
	if len(serverKey) != SecretSize {
		return nil, fmt.Errorf("dedup: serverKey must be %d bytes, got %d", SecretSize, len(serverKey))
	}
	block, err := aes.NewCipher(serverKey)
	if err != nil {
		return nil, fmt.Errorf("dedup: aes new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("dedup: gcm new: %w", err)
	}
	if len(sealed) < aead.NonceSize() {
		return nil, fmt.Errorf("dedup: sealed envelope too short")
	}
	nonce, ct := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	out, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("dedup: unseal master: %w", err)
	}
	return out, nil
}

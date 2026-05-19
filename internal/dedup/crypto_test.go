package dedup

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestCryptoChunkIDDeterministic(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	plain := []byte("hello world")
	id1 := ChunkID(key, plain)
	id2 := ChunkID(key, plain)
	if id1 != id2 {
		t.Fatal("ChunkID is not deterministic")
	}
	if id1 == (ID{}) {
		t.Fatal("ChunkID returned zero value")
	}
}

func TestCryptoChunkIDKeyMatters(t *testing.T) {
	a := bytes.Repeat([]byte{0x01}, 32)
	b := bytes.Repeat([]byte{0x02}, 32)
	plain := []byte("hello world")
	if ChunkID(a, plain) == ChunkID(b, plain) {
		t.Fatal("ChunkID with different keys produced same ID")
	}
}

func TestCryptoEncryptDecryptRoundTrip(t *testing.T) {
	master := bytes.Repeat([]byte{0xaa}, 32)
	plain := []byte("the quick brown fox jumps over the lazy dog")
	id := ChunkID(master, plain)
	ct, err := EncryptChunk(master, id, plain)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecryptChunk(master, id, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatalf("round-trip mismatch: got %q want %q", out, plain)
	}
}

func TestCryptoEncryptDecryptEmpty(t *testing.T) {
	master := bytes.Repeat([]byte{0xaa}, 32)
	id := ChunkID(master, nil)
	ct, err := EncryptChunk(master, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecryptChunk(master, id, ct)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("empty round-trip returned %d bytes", len(out))
	}
}

func TestCryptoDecryptTamperFails(t *testing.T) {
	master := bytes.Repeat([]byte{0xaa}, 32)
	plain := []byte("hello world")
	id := ChunkID(master, plain)
	ct, err := EncryptChunk(master, id, plain)
	if err != nil {
		t.Fatal(err)
	}
	ct[len(ct)/2] ^= 0x01
	if _, err := DecryptChunk(master, id, ct); err == nil {
		t.Fatal("DecryptChunk accepted tampered ciphertext")
	}
}

func TestCryptoSealUnsealRoundTrip(t *testing.T) {
	serverKey := bytes.Repeat([]byte{0x33}, 32)
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatal(err)
	}
	sealed, err := SealMaster(serverKey, master)
	if err != nil {
		t.Fatal(err)
	}
	out, err := UnsealMaster(serverKey, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, master) {
		t.Fatal("seal/unseal round-trip mismatch")
	}
}

func TestCryptoUnsealWrongKeyFails(t *testing.T) {
	sk1 := bytes.Repeat([]byte{0x33}, 32)
	sk2 := bytes.Repeat([]byte{0x44}, 32)
	master := bytes.Repeat([]byte{0x77}, 32)
	sealed, err := SealMaster(sk1, master)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnsealMaster(sk2, sealed); err == nil {
		t.Fatal("UnsealMaster accepted wrong key")
	}
}

func TestCryptoDeriveKeysDeterministic(t *testing.T) {
	master := bytes.Repeat([]byte{0x55}, 32)
	a1 := DeriveChunkHashKey(master)
	a2 := DeriveChunkHashKey(master)
	if !bytes.Equal(a1, a2) {
		t.Fatal("DeriveChunkHashKey not deterministic")
	}
	s1 := DeriveSplitterSecret(master)
	s2 := DeriveSplitterSecret(master)
	if !bytes.Equal(s1, s2) {
		t.Fatal("DeriveSplitterSecret not deterministic")
	}
	if bytes.Equal(a1, s1) {
		t.Fatal("chunk-hash and splitter keys must differ (different info strings)")
	}
}

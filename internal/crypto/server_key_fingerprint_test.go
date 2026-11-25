package crypto

import "testing"

func TestServerKeyFingerprint(t *testing.T) {
	a := make([]byte, ServerKeySize)
	b := make([]byte, ServerKeySize)
	b[0] = 1
	fa, fb := ServerKeyFingerprint(a), ServerKeyFingerprint(b)
	if len(fa) != 16 {
		t.Fatalf("fingerprint length = %d, want 16", len(fa))
	}
	if fa == fb {
		t.Fatal("different keys must have different fingerprints")
	}
	if fa != ServerKeyFingerprint(a) {
		t.Fatal("fingerprint must be stable")
	}
	if ServerKeyFingerprint(nil) != "" {
		t.Fatal("an empty key has no fingerprint")
	}
}

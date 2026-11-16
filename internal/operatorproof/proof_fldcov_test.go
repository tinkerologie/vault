package operatorproof

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func fldcovEnvelope(t *testing.T, key [32]byte, action Action) Envelope {
	t.Helper()
	payload, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key[:])
	mac.Write(payload)
	return Envelope{Payload: string(payload), Signature: hex.EncodeToString(mac.Sum(nil))}
}

func TestVerifierRefusesNilAndExpiresUsedNonces(t *testing.T) {
	var missing *Verifier
	if _, ok := missing.Verify(Envelope{}, "approve", "x"); ok {
		t.Fatal("nil verifier accepted a proof")
	}

	key := [32]byte{3}
	clock := time.Unix(1_800_000_000, 0)
	verifier := New(key, func() time.Time { return clock })
	action := Action{Version: 1, Action: "approve", ID: "req", Timestamp: clock.Unix(), Nonce: fmt.Sprintf("%032x", 1)}
	if _, ok := verifier.Verify(fldcovEnvelope(t, key, action), "approve", "req"); !ok {
		t.Fatal("fresh proof rejected")
	}
	if _, ok := verifier.Verify(fldcovEnvelope(t, key, action), "approve", "req"); ok {
		t.Fatal("replayed proof accepted")
	}
	clock = clock.Add(2 * maxProofAge)
	next := action
	next.Timestamp, next.Nonce = clock.Unix(), fmt.Sprintf("%032x", 2)
	if _, ok := verifier.Verify(fldcovEnvelope(t, key, next), "approve", "req"); !ok {
		t.Fatal("later proof rejected")
	}
	verifier.mu.Lock()
	_, kept := verifier.used[action.Nonce]
	verifier.mu.Unlock()
	if kept {
		t.Fatal("expired nonce was not released")
	}
}

// Package operatorproof verifies finite actions signed by the native Unraid gateway.
package operatorproof

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	maxProofAge   = 60 * time.Second
	maxUsedNonces = 4096
)

// Envelope transports the exact JSON bytes covered by the signature. It is
// transient and can contain a passphrase.
type Envelope struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type Action struct {
	Version      int    `json:"version"`
	Action       string `json:"action"`
	ID           string `json:"id"`
	ReviewDigest string `json:"review_digest"`
	Timestamp    int64  `json:"timestamp"`
	Nonce        string `json:"nonce"`
	Operator     string `json:"operator"`
	Passphrase   string `json:"passphrase"`
}

type Verifier struct {
	key  [32]byte
	now  func() time.Time
	mu   sync.Mutex
	used map[string]time.Time
}

func New(key [32]byte, now func() time.Time) *Verifier {
	if now == nil {
		now = time.Now
	}
	return &Verifier{key: key, now: now, used: make(map[string]time.Time)}
}

// Verify consumes a proof once. A duplicate operator submission arrives with
// a fresh proof and the existing durable Restore Run can then be returned.
func (v *Verifier) Verify(envelope Envelope, action, id string) (Action, bool) {
	var result Action
	if v == nil {
		return result, false
	}
	signature, err := hex.DecodeString(envelope.Signature)
	mac := hmac.New(sha256.New, v.key[:])
	mac.Write([]byte(envelope.Payload))
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) ||
		json.Unmarshal([]byte(envelope.Payload), &result) != nil ||
		result.Version != 1 || result.Action != action || result.ID != id ||
		!validHex(result.Nonce, 32) || len(result.Operator) > 128 ||
		strings.ContainsFunc(result.Operator, unicode.IsControl) {
		return Action{}, false
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	signedAt := time.Unix(result.Timestamp, 0)
	if signedAt.After(now.Add(5*time.Second)) || !signedAt.Add(maxProofAge).After(now) {
		return Action{}, false
	}
	for nonce, expiry := range v.used {
		if !expiry.After(now) {
			delete(v.used, nonce)
		}
	}
	if _, used := v.used[result.Nonce]; used || len(v.used) >= maxUsedNonces {
		return Action{}, false
	}
	v.used[result.Nonce] = signedAt.Add(maxProofAge)
	return result, true
}

func validHex(value string, length int) bool {
	_, err := hex.DecodeString(value)
	return len(value) == length && err == nil && strings.ToLower(value) == value
}

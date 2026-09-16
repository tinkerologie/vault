package runner

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// manifestEnvelope represents an encrypted manifest.json envelope on storage.
// The VaultManifestEnc discriminator field allows unambiguous detection of
// encrypted envelopes vs legacy plaintext manifests (issue #325).
type manifestEnvelope struct {
	VaultManifestEnc int    `json:"vault_manifest_enc"` // Always 1
	Key              string `json:"key"`                // "dedup" or "age"
	Algo             string `json:"algo"`               // "aes-256-gcm" or "age"
	Payload          string `json:"payload"`            // Base64-encoded ciphertext
}

type manifestEnvelopeProbe struct {
	VaultManifestEnc int `json:"vault_manifest_enc"`
}

// detectManifestEnvelope probes raw manifest data to determine whether it is an
// encrypted envelope (vault_manifest_enc == 1). If true, it decodes and returns the envelope.
func detectManifestEnvelope(raw []byte) (bool, manifestEnvelope) {
	var probe manifestEnvelopeProbe
	if err := json.Unmarshal(raw, &probe); err != nil || probe.VaultManifestEnc != 1 {
		return false, manifestEnvelope{}
	}
	var env manifestEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return false, manifestEnvelope{}
	}
	return true, env
}

// encodeManifestEnvelope creates a JSON-encoded manifest envelope wrapping ciphertext.
func encodeManifestEnvelope(key, algo string, ciphertext []byte) ([]byte, error) {
	env := manifestEnvelope{
		VaultManifestEnc: 1,
		Key:              key,
		Algo:             algo,
		Payload:          base64.StdEncoding.EncodeToString(ciphertext),
	}
	return json.MarshalIndent(env, "", "  ")
}

// decodeManifestEnvelope decodes the base64 ciphertext payload from the envelope.
func decodeManifestEnvelope(env manifestEnvelope) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, fmt.Errorf("decode manifest payload: %w", err)
	}
	return b, nil
}

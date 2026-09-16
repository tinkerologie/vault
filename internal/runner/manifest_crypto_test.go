package runner

import (
	"bytes"
	"testing"
)

func TestManifestEnvelopeDetect(t *testing.T) {
	// Plaintext manifest
	plainJSON := []byte(`{"version":1,"job_name":"test-job","backup_type":"full"}`)
	isEnv, _ := detectManifestEnvelope(plainJSON)
	if isEnv {
		t.Fatal("detectManifestEnvelope reported true for plaintext manifest")
	}

	// Empty input
	isEnv, _ = detectManifestEnvelope(nil)
	if isEnv {
		t.Fatal("detectManifestEnvelope reported true for nil input")
	}

	// Malformed JSON
	isEnv, _ = detectManifestEnvelope([]byte("not json"))
	if isEnv {
		t.Fatal("detectManifestEnvelope reported true for malformed JSON")
	}

	// Unknown envelope version
	unknownVer := []byte(`{"vault_manifest_enc":2,"key":"dedup","algo":"aes-256-gcm","payload":"dGVzdA=="}`)
	isEnv, _ = detectManifestEnvelope(unknownVer)
	if isEnv {
		t.Fatal("detectManifestEnvelope reported true for unknown envelope version")
	}

	// Probe succeeds but full unmarshal fails
	mismatchedJSON := []byte(`{"vault_manifest_enc":1,"key":123}`)
	isEnv, _ = detectManifestEnvelope(mismatchedJSON)
	if isEnv {
		t.Fatal("detectManifestEnvelope reported true for type-mismatched JSON")
	}

	// Valid envelope
	rawCipher := []byte("secret ciphertext bytes")
	envBytes, err := encodeManifestEnvelope("dedup", "aes-256-gcm", rawCipher)
	if err != nil {
		t.Fatalf("encodeManifestEnvelope: %v", err)
	}

	isEnv, env := detectManifestEnvelope(envBytes)
	if !isEnv {
		t.Fatal("detectManifestEnvelope failed to detect valid envelope")
	}
	if env.VaultManifestEnc != 1 {
		t.Fatalf("expected VaultManifestEnc=1, got %d", env.VaultManifestEnc)
	}
	if env.Key != "dedup" || env.Algo != "aes-256-gcm" {
		t.Fatalf("unexpected key/algo: %s/%s", env.Key, env.Algo)
	}

	decPayload, err := decodeManifestEnvelope(env)
	if err != nil {
		t.Fatalf("decodeManifestEnvelope: %v", err)
	}
	if !bytes.Equal(decPayload, rawCipher) {
		t.Fatalf("payload mismatch: got %q want %q", decPayload, rawCipher)
	}
}

func TestManifestEnvelopeDecodeCorruptBase64(t *testing.T) {
	env := manifestEnvelope{
		VaultManifestEnc: 1,
		Key:              "age",
		Algo:             "age",
		Payload:          "not-valid-base64!@#$",
	}
	if _, err := decodeManifestEnvelope(env); err == nil {
		t.Fatal("decodeManifestEnvelope expected error on invalid base64")
	}
}

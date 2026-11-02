// Package credentials owns how storage and replication configuration secrets
// are sealed at rest and masked in API, MCP and diagnostics output.
//
// Secrets are sealed field by field, so the non-secret parts of a config (host,
// path, username) stay readable when vault.key is missing. A config read back
// with a sealed token still in it is "locked": it cannot authenticate, but the
// ciphertext survives until the original vault.key returns or the user retypes
// the credential.
package credentials

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/tinkerologie/vault/internal/crypto"
)

// RedactionMarker is the value API and MCP reads return in place of a secret.
// Writing it back keeps the stored secret.
const RedactionMarker = "••••••••"

const sealedPrefix = "vault-sealed:v1:"

// ErrLocked reports a config whose secrets this server's vault.key cannot open.
var ErrLocked = errors.New("stored credentials cannot be opened with this server's vault.key: restore the original vault.key or re-enter them")

// ErrSecretRetarget reports an edit that would send a stored secret to a
// different destination without the secret being entered again.
var ErrSecretRetarget = errors.New("re-enter the password or secret when changing the server this destination connects to")

// maskedKeys are masked on every read path.
var maskedKeys = map[string]bool{
	"password":          true,
	"secret_key":        true,
	"secret_access_key": true,
	"passphrase":        true,
	"key_file":          true,
	"refresh_token":     true,
	"client_secret":     true,
	"api_key":           true,
	"private_key":       true,
	"token":             true,
}

// sealedKeys are encrypted at rest. The S3 access key is sealed but stays
// visible in the edit form, which needs it to show who the destination logs in as.
func isSealedKey(k string) bool { return maskedKeys[k] || k == "access_key" }

// IsMaskedKey reports whether a config field is hidden from read responses.
func IsMaskedKey(k string) bool { return maskedKeys[k] }

func parseObject(config string) (map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(config))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil || dec.More() {
		return nil, false
	}
	return m, true
}

// marshal encodes a map parsed by parseObject. Decoded JSON always re-encodes.
func marshal(m map[string]any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(m)
	return strings.TrimSuffix(buf.String(), "\n")
}

func isSealed(s string) bool { return strings.HasPrefix(s, sealedPrefix) }

// hasUserinfo reports whether s is a URL carrying inline credentials.
func hasUserinfo(s string) bool {
	if !strings.Contains(s, "://") || !strings.Contains(s, "@") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.User != nil
}

// StripUserinfo removes inline credentials from a URL string. A value that
// looks like a credentialed URL but does not parse is dropped entirely.
func StripUserinfo(s string) string {
	if !strings.Contains(s, "://") || !strings.Contains(s, "@") {
		return s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	if u.User == nil {
		return s
	}
	u.User = nil
	return u.String()
}

// Seal encrypts a config's secrets for storage. Values already sealed are kept,
// so sealing is idempotent and a locked secret survives an unrelated edit. A
// redaction marker is never a real secret and is dropped. With no key the
// secrets are left as they are, which only key-less tooling and tests do.
func Seal(key []byte, config string) (string, error) {
	m, ok := parseObject(config)
	if !ok {
		return config, nil
	}
	changed := false
	for k, v := range m {
		s, isStr := v.(string)
		if !isStr || s == "" || isSealed(s) {
			continue
		}
		if isSealedKey(k) && s == RedactionMarker {
			delete(m, k)
			changed = true
			continue
		}
		if len(key) == 0 || (!isSealedKey(k) && !hasUserinfo(s)) {
			continue
		}
		token, err := crypto.Seal(key, s)
		if err != nil {
			return "", fmt.Errorf("seal %s: %w", k, err)
		}
		m[k] = sealedPrefix + token
		changed = true
	}
	if !changed {
		return config, nil
	}
	return marshal(m), nil
}

// Open decrypts a stored config. A secret the key cannot open stays sealed, so
// the row reads as locked rather than failing the whole listing. Plaintext
// configs written before sealing pass through unchanged.
func Open(key []byte, stored string) string {
	if !strings.Contains(stored, sealedPrefix) {
		return stored
	}
	m, ok := parseObject(stored)
	if !ok {
		return stored
	}
	for k, v := range m {
		s, isStr := v.(string)
		if !isStr || !isSealed(s) || len(key) == 0 {
			continue
		}
		if plain, err := crypto.Unseal(key, strings.TrimPrefix(s, sealedPrefix)); err == nil {
			m[k] = plain
		}
	}
	return marshal(m)
}

// IsLocked reports whether a config read from the database still holds a secret
// that could not be opened. Such a config must never reach a remote endpoint.
func IsLocked(config string) bool {
	if !strings.Contains(config, sealedPrefix) {
		return false
	}
	m, ok := parseObject(config)
	if !ok {
		return true
	}
	for _, v := range m {
		if s, isStr := v.(string); isStr && isSealed(s) {
			return true
		}
	}
	return false
}

// Mask returns a config that is safe to show: secrets and sealed values become
// the marker and URL credentials are removed, at any depth. Malformed input may
// hold anything, so it is replaced with an empty object.
func Mask(config string) string {
	m, ok := parseObject(config)
	if !ok {
		return "{}"
	}
	return marshal(maskMap(m))
}

func maskMap(m map[string]any) map[string]any {
	for k, v := range m {
		if s, isStr := v.(string); isStr && s != "" && IsMaskedKey(k) {
			m[k] = RedactionMarker
			continue
		}
		m[k] = maskValue(v)
	}
	return m
}

func maskValue(v any) any {
	switch val := v.(type) {
	case string:
		if isSealed(val) {
			return RedactionMarker
		}
		return StripUserinfo(val)
	case map[string]any:
		return maskMap(val)
	case []any:
		for i := range val {
			val[i] = maskValue(val[i])
		}
	}
	return v
}

// Preserve restores what Mask hid when a client sends a config back unchanged:
// a marker takes the stored value, and a URL equal to the stored URL without its
// credentials takes the stored URL. Anything else passes through, including
// input that is not JSON, which the adapter validator reports in context.
func Preserve(incoming, existing string) string {
	in, ok := parseObject(incoming)
	if !ok {
		return incoming
	}
	ex, ok := parseObject(existing)
	if !ok {
		return incoming
	}
	changed := false
	for k, v := range in {
		s, isStr := v.(string)
		if !isStr {
			continue
		}
		old, isOldStr := ex[k].(string)
		if !isOldStr {
			continue
		}
		restoreMarker := s == RedactionMarker && (IsMaskedKey(k) || isSealed(old))
		restoreURL := hasUserinfo(old) && s == StripUserinfo(old)
		if restoreMarker || restoreURL {
			in[k] = old
			changed = true
		}
	}
	if !changed {
		return incoming
	}
	return marshal(in)
}

// destinationFields lists, per storage type, the config fields that decide who
// receives the stored secret. Fields on the same server (user, share, base path,
// a WebDAV URL's path) are left out: the secret still goes where it was entered.
var destinationFields = map[string][]string{
	"local":  {},
	"nfs":    {"host"},
	"sftp":   {"host", "port", "host_key", "known_hosts_file"},
	"smb":    {"host", "port"},
	"webdav": {"url", "insecure_skip_verify"},
	"s3":     {"endpoint", "bucket", "region"},
}

var defaultPorts = map[string]string{"sftp": "22", "smb": "445"}

// destinationValue normalizes one field so a cosmetic difference, such as host
// case or an omitted default port, does not count as a new destination.
func destinationValue(storageType, field string, v any) string {
	s := ""
	if v != nil {
		s = strings.TrimSpace(fmt.Sprint(v))
	}
	switch field {
	case "host":
		return strings.ToLower(s)
	case "port":
		if s == "" || s == "0" {
			return defaultPorts[storageType]
		}
	case "insecure_skip_verify":
		if s == "false" {
			return ""
		}
	case "url", "endpoint":
		return originOf(s)
	}
	return s
}

// originOf reduces a URL to its scheme, host and port.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.ToLower(raw)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[strings.ToLower(u.Scheme)]
	}
	return strings.ToLower(u.Scheme + "://" + u.Hostname() + ":" + port)
}

func destinationChanged(storageType string, in, ex map[string]any) bool {
	fields, known := destinationFields[storageType]
	if !known {
		// An unknown type gets the strict rule: any visible field counts.
		for k, v := range in {
			if !IsMaskedKey(k) && v != ex[k] {
				return true
			}
		}
		return false
	}
	for _, f := range fields {
		// A masked value is the stored value, unchanged.
		if s, _ := in[f].(string); s == RedactionMarker {
			continue
		}
		if destinationValue(storageType, f, in[f]) != destinationValue(storageType, f, ex[f]) {
			return true
		}
	}
	return false
}

// MergeStorageEdit applies Preserve to an edited storage config, but only where
// the stored secret keeps going to the same destination. Otherwise anyone who
// can edit a destination could point it at their own server and collect the
// secret. A retyped secret is always accepted.
func MergeStorageEdit(storedType, newType, incoming, existing string) (string, error) {
	in, inOK := parseObject(incoming)
	ex, exOK := parseObject(existing)
	if !inOK || !exOK {
		return Preserve(incoming, existing), nil
	}
	reusesSecret := false
	for k, v := range in {
		old, isOldStr := ex[k].(string)
		if s, isStr := v.(string); isStr && s == RedactionMarker && isOldStr && old != "" && (IsMaskedKey(k) || isSealed(old)) {
			reusesSecret = true
		}
	}
	if reusesSecret && (storedType != newType || destinationChanged(newType, in, ex)) {
		return "", ErrSecretRetarget
	}
	return Preserve(incoming, existing), nil
}

// HasMarker reports whether a config still holds a masked value at its top level.
func HasMarker(config string) bool {
	m, ok := parseObject(config)
	if !ok {
		return false
	}
	for _, v := range m {
		if s, isStr := v.(string); isStr && s == RedactionMarker {
			return true
		}
	}
	return false
}

// IsPlaintextRemote reports whether rawURL sends traffic unencrypted to a host
// other than this machine.
func IsPlaintextRemote(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

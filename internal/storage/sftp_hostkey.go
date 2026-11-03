package storage

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync/atomic"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyPinner records the first host key seen for an address and returns
// the one on record: the given fingerprint for a new address, or the existing
// pin. It must be atomic, because a pool opens connections concurrently.
type HostKeyPinner func(address, fingerprint string) (string, error)

var hostKeyPinner atomic.Pointer[HostKeyPinner]

// SetHostKeyPinner enables trust-on-first-use for SFTP destinations that have
// neither host_key nor known_hosts_file. Commands with a Vault database call
// it; without one (vault recover) such a destination fails with the server's
// fingerprint and how to configure it.
func SetHostKeyPinner(p HostKeyPinner) {
	if p == nil {
		hostKeyPinner.Store(nil)
		return
	}
	hostKeyPinner.Store(&p)
}

// parseHostKeyFingerprint accepts the OpenSSH form (SHA256:<base64>, as
// printed by ssh-keygen -l) and the hex form (with or without SHA256:).
func parseHostKeyFingerprint(s string) ([]byte, error) {
	v := strings.TrimSpace(s)
	body := strings.TrimPrefix(v, "SHA256:")
	if len(body) == hex.EncodedLen(sha256.Size) {
		if sum, err := hex.DecodeString(body); err == nil {
			return sum, nil
		}
	}
	if body != v {
		if sum, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(body, "=")); err == nil && len(sum) == sha256.Size {
			return sum, nil
		}
	}
	return nil, fmt.Errorf("sftp host_key %q is not a SHA-256 fingerprint (run ssh-keygen -lf on the server's host key and copy the SHA256:... value)", s)
}

func hostKeySum(key ssh.PublicKey) []byte {
	sum := sha256.Sum256(key.Marshal())
	return sum[:]
}

// newHostKeyCallback returns the host-key check for config, in this order:
// known_hosts_file, host_key, then trust-on-first-use. It never accepts a key
// unverified: an unusable known_hosts_file or host_key is an error rather than
// a reason to try the next option.
func newHostKeyCallback(config SFTPConfig) (ssh.HostKeyCallback, error) {
	if config.KnownHostsFile != "" {
		cb, err := knownhosts.New(config.KnownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("sftp known_hosts_file: %w", err)
		}
		return cb, nil
	}
	if config.HostKey != "" {
		want, err := parseHostKeyFingerprint(config.HostKey)
		if err != nil {
			return nil, err
		}
		return func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if subtle.ConstantTimeCompare(hostKeySum(key), want) != 1 {
				return fmt.Errorf("host key mismatch: destination expects %s, server presented %s", config.HostKey, ssh.FingerprintSHA256(key))
			}
			return nil
		}, nil
	}
	return pinnedHostKeyCallback(config), nil
}

// pinnedHostKeyCallback implements trust-on-first-use. The first key an
// address presents is recorded, and any later different key is refused. The
// check runs before authentication, so a refused server never sees a password.
func pinnedHostKeyCallback(config SFTPConfig) ssh.HostKeyCallback {
	address := net.JoinHostPort(config.Host, fmt.Sprintf("%d", config.Port))
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		p := hostKeyPinner.Load()
		if p == nil {
			return fmt.Errorf("sftp destination %s has no host key configured: check that %s is this server's key, then set \"host_key\": %q in the destination settings", address, got, got)
		}
		pinned, err := (*p)(address, got)
		if err != nil {
			return fmt.Errorf("record sftp host key for %s: %w", address, err)
		}
		if pinned != got {
			return fmt.Errorf("sftp host key for %s changed: first seen %s, server presented %s. This can mean an attack. If the server was reinstalled, confirm the new key with ssh-keygen -lf and enter it as the destination's Host Key Fingerprint", address, pinned, got)
		}
		return nil
	}
}

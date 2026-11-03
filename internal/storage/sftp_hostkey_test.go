package storage

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// memPinner is the first-use store the daemon backs with its database.
func memPinner(t *testing.T) map[string]string {
	t.Helper()
	var mu sync.Mutex
	pins := map[string]string{}
	SetHostKeyPinner(func(address, fingerprint string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if got, ok := pins[address]; ok {
			return got, nil
		}
		pins[address] = fingerprint
		return fingerprint, nil
	})
	t.Cleanup(func() { SetHostKeyPinner(nil) })
	return pins
}

func TestNewSFTPAdapter_RefusesUnusableVerification(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(bad, []byte("not a known_hosts line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, fp := genTestSSHKey(t)
	for name, cfg := range map[string]SFTPConfig{
		"missing known_hosts":             {Host: "h", KnownHostsFile: "/no/such/file"},
		"malformed known_hosts":           {Host: "h", KnownHostsFile: bad},
		"valid host_key does not rescue":  {Host: "h", KnownHostsFile: "/no/such/file", HostKey: fp},
		"malformed host_key":              {Host: "h", HostKey: "deadbeef"},
		"base64 without the SHA256 label": {Host: "h", HostKey: strings.Repeat("A", 43)},
	} {
		if a, err := NewSFTPAdapter(cfg); err == nil {
			_ = a.Close()
			t.Errorf("%s: adapter created", name)
		}
	}
}

func TestParseHostKeyFingerprint_Forms(t *testing.T) {
	t.Parallel()
	pub, hexFP := genTestSSHKey(t)
	sum := sha256.Sum256(pub.Marshal())
	for _, in := range []string{
		hexFP, "SHA256:" + hexFP, strings.ToUpper(hexFP), ssh.FingerprintSHA256(pub),
		" " + ssh.FingerprintSHA256(pub) + "\n", "SHA256:" + base64.StdEncoding.EncodeToString(sum[:]),
	} {
		got, err := parseHostKeyFingerprint(in)
		if err != nil || string(got) != string(sum[:]) {
			t.Errorf("%q: %x, %v", in, got, err)
		}
	}
}

func TestPinnedHostKey_FirstUseThenRefusesChange(t *testing.T) {
	pins := memPinner(t)
	a, err := NewSFTPAdapter(SFTPConfig{Host: "nas", User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	first, _ := genTestSSHKey(t)
	if err := a.hostKey("nas:22", &net.TCPAddr{}, first); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if pins["nas:22"] != ssh.FingerprintSHA256(first) {
		t.Fatalf("pins = %v", pins)
	}
	if err := a.hostKey("nas:22", &net.TCPAddr{}, first); err != nil {
		t.Fatalf("same key: %v", err)
	}
	other, _ := genTestSSHKey(t)
	err = a.hostKey("nas:22", &net.TCPAddr{}, other)
	if err == nil || !strings.Contains(err.Error(), ssh.FingerprintSHA256(other)) || !strings.Contains(err.Error(), "Host Key Fingerprint") {
		t.Fatalf("changed key: %v", err)
	}
}

func TestPinnedHostKey_ExplicitKeyOverridesPin(t *testing.T) {
	pins := memPinner(t)
	old, _ := genTestSSHKey(t)
	pins["nas:22"] = ssh.FingerprintSHA256(old)
	replacement, _ := genTestSSHKey(t)
	a, err := NewSFTPAdapter(SFTPConfig{Host: "nas", User: "u", HostKey: ssh.FingerprintSHA256(replacement)})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.hostKey("nas:22", &net.TCPAddr{}, replacement); err != nil {
		t.Fatalf("configured key refused: %v", err)
	}
}

func TestPinnedHostKey_NoStoreFailsWithTheFix(t *testing.T) {
	SetHostKeyPinner(nil)
	a, err := NewSFTPAdapter(SFTPConfig{Host: "nas", User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	key, _ := genTestSSHKey(t)
	err = a.hostKey("nas:22", &net.TCPAddr{}, key)
	if err == nil || !strings.Contains(err.Error(), ssh.FingerprintSHA256(key)) || !strings.Contains(err.Error(), "host_key") {
		t.Fatalf("err = %v", err)
	}
}

func TestPinnedHostKey_StoreErrorFailsClosed(t *testing.T) {
	boom := errors.New("db locked")
	SetHostKeyPinner(func(string, string) (string, error) { return "", boom })
	t.Cleanup(func() { SetHostKeyPinner(nil) })
	a, err := NewSFTPAdapter(SFTPConfig{Host: "nas", User: "u"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	key, _ := genTestSSHKey(t)
	if err := a.hostKey("nas:22", &net.TCPAddr{}, key); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// The real handshake: an existing destination with no key keeps working after
// the upgrade, and an impostor at the same address is then refused before it
// can see the password.
func TestPinnedHostKey_OverSSH(t *testing.T) {
	memPinner(t)
	srv := startSFTP458Server(t)
	cfg := SFTPConfig{Host: "127.0.0.1", Port: srv.port, User: sftp458User, Password: sftp458Password, BasePath: t.TempDir()}
	a, err := NewSFTPAdapter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.TestConnection(); err != nil {
		t.Fatalf("first connection: %v", err)
	}

	SetHostKeyPinner(func(string, string) (string, error) { return "SHA256:someotherkey", nil })
	b, err := NewSFTPAdapter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.TestConnection(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed key: %v", err)
	}
}

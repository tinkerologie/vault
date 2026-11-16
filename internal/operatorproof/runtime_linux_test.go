//go:build linux

package operatorproof

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRuntimeRequiresRootAndInstalledFiles(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "native-marker")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireNativeRuntime(0, marker); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		uid  int
		file string
	}{
		{501, marker},
		{0, filepath.Join(root, "missing")},
		{0, root},
	} {
		if err := requireNativeRuntime(scenario.uid, scenario.file); err == nil {
			t.Fatalf("invalid native runtime was accepted for UID %d and %s", scenario.uid, scenario.file)
		}
	}
}

func TestRuntimeVerifierKeyIsPrivateAndRotates(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "runtime")
	path := filepath.Join(dir, "approval.key")
	owner := uint32(os.Geteuid()) // #nosec G115 -- the OS returns a nonnegative UID.
	var previous []byte
	for range 2 {
		verifier, err := createRuntimeVerifier(path, owner)
		if err != nil {
			t.Fatal(err)
		}
		key, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(key, verifier.key[:]) || bytes.Equal(key, previous) {
			t.Fatalf("runtime key was unavailable, mismatched or reused: %v", err)
		}
		previous = key
		if info, err := os.Stat(path); err != nil || info.Mode() != 0o600 {
			t.Fatalf("runtime key must be private: %v %v", info, err)
		}
	}
	if _, err := createRuntimeVerifier(path, owner+1); err == nil {
		t.Fatal("accepted the wrong runtime directory owner")
	}
	link := filepath.Join(root, "linked-runtime")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := createRuntimeVerifier(filepath.Join(link, "approval.key"), owner); err == nil {
		t.Fatal("accepted a symlink as the runtime directory")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := createRuntimeVerifier(path, owner); err == nil {
		t.Fatal("accepted a runtime directory that other users can traverse")
	}
}

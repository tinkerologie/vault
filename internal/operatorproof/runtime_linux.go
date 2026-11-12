//go:build linux

package operatorproof

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// NewRuntime writes a boot-random key for the installed native PHP plugin.
func NewRuntime() (*Verifier, error) {
	const path = "/run/vault-operator/approval.key"
	if err := requireNativeRuntime(os.Geteuid(),
		"/etc/unraid-version",
		"/usr/local/emhttp/plugins/dynamix/include/local_prepend.php",
		"/usr/local/emhttp/plugins/vault/include/restore-request.php",
	); err != nil {
		return nil, err
	}
	return createRuntimeVerifier(path, 0)
}

func requireNativeRuntime(uid int, nativeFiles ...string) error {
	if uid != 0 {
		return errors.New("native approval requires a root-owned runtime directory")
	}
	for _, nativeFile := range nativeFiles {
		info, err := os.Stat(nativeFile)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("native Unraid approval requires the installed Vault plugin")
		}
	}
	return nil
}

func createRuntimeVerifier(path string, owner uint32) (*Verifier, error) {
	dir := filepath.Dir(path)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create operator approval directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != owner || info.Mode() != os.ModeDir|0o700 {
		return nil, errors.New("unsafe operator approval directory")
	}
	var key [32]byte
	rand.Read(key[:])
	f, err := os.CreateTemp(dir, "key-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(key[:]); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return nil, err
	}
	return New(key, nil), nil
}

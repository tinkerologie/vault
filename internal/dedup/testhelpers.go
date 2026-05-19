package dedup

// This file intentionally lives outside *_test.go so the helpers below are
// importable from cross-package tests (e.g. internal/engine/folder_test.go).
// The helpers are inert at runtime — they have no entry points and ship as
// dead weight in the production binary (~80 LOC of in-memory map operations).
// Tradeoff accepted to keep test wiring trivial across packages.

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// FakeAdapter is an in-memory storage.Adapter for tests. It supports
// Write / Read / ReadRange / Delete / List / Stat / TestConnection so it can
// stand in for the real LocalAdapter throughout dedup-package tests and
// cross-package consumers (engine handlers, runner, GC).
type FakeAdapter struct{ files map[string][]byte }

// NewFakeAdapter constructs an empty in-memory adapter.
func NewFakeAdapter() *FakeAdapter { return &FakeAdapter{files: map[string][]byte{}} }

// Write stores the reader's contents at path, overwriting any prior data.
func (f *FakeAdapter) Write(path string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.files[path] = b
	return nil
}

// Read returns the full contents at path.
func (f *FakeAdapter) Read(path string) (io.ReadCloser, error) {
	b, ok := f.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// ReadRange returns a byte range [offset, offset+length) at path.
func (f *FakeAdapter) ReadRange(path string, offset, length int64) (io.ReadCloser, error) {
	b, ok := f.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	if offset >= int64(len(b)) {
		return nil, io.ErrUnexpectedEOF
	}
	end := offset + length
	if end > int64(len(b)) {
		end = int64(len(b))
	}
	return io.NopCloser(bytes.NewReader(b[offset:end])), nil
}

// Delete removes the file at path if present (no error if missing).
func (f *FakeAdapter) Delete(path string) error { delete(f.files, path); return nil }

// List returns all stored files whose paths start with prefix.
func (f *FakeAdapter) List(prefix string) ([]storage.FileInfo, error) {
	out := []storage.FileInfo{}
	for k, v := range f.files {
		if strings.HasPrefix(k, prefix) {
			out = append(out, storage.FileInfo{Path: k, Size: int64(len(v))})
		}
	}
	return out, nil
}

// Stat returns the FileInfo for path, or an error if missing.
func (f *FakeAdapter) Stat(path string) (storage.FileInfo, error) {
	b, ok := f.files[path]
	if !ok {
		return storage.FileInfo{}, errors.New("not found")
	}
	return storage.FileInfo{Path: path, Size: int64(len(b))}, nil
}

// TestConnection is a no-op (in-memory adapter is always reachable).
func (f *FakeAdapter) TestConnection() error { return nil }

// NewTestRepoForEngine spins up a fresh DB + in-memory adapter + InitRepo'd
// Repo for cross-package tests (e.g. internal/engine/folder_test.go). Returns
// the Repo, the underlying fake adapter, and a cleanup callback. The caller
// owns the cleanup — it must be invoked (typically via defer) to close the DB.
func NewTestRepoForEngine(t *testing.T) (*Repo, *FakeAdapter, func()) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}", DedupEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	serverKey := bytes.Repeat([]byte{0xee}, SecretSize)
	a := NewFakeAdapter()
	r, err := InitRepo(d, a, destID, serverKey)
	if err != nil {
		t.Fatal(err)
	}
	return r, a, func() { d.Close() }
}

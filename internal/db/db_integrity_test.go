package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIntegrityCheckHealthy(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "ok.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := d.IntegrityCheck(); err != nil {
		t.Errorf("healthy DB returned error: %v", err)
	}
}

func TestIntegrityCheckCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO settings (key, value) VALUES ('x', 'y')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Corrupt the file: overwrite a chunk in the middle.
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open for corrupt: %v", err)
	}
	junk := make([]byte, 1024)
	for i := range junk {
		junk[i] = 0xff
	}
	if _, err := f.WriteAt(junk, 4096); err != nil {
		t.Fatalf("corrupt write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close corrupted: %v", err)
	}

	d2, err := Open(path)
	if err != nil {
		// Open itself failing is an acceptable "unhealthy" outcome.
		return
	}
	defer d2.Close()
	if err := d2.IntegrityCheck(); err == nil {
		t.Errorf("corrupt DB returned nil from IntegrityCheck")
	}
}

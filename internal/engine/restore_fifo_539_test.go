//go:build unix

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// callWithTimeout fails instead of hanging when the open blocks on a FIFO.
func callWithTimeout(t *testing.T, fn func() (*os.File, error)) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		f, err := fn()
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("open blocked on a FIFO")
		return nil
	}
}

func TestRestoreOpenRefusesFIFO(t *testing.T) {
	t.Parallel()
	openers := map[string]func(string) (*os.File, error){
		"openRestoreDestination": func(p string) (*os.File, error) { return openRestoreDestination(p, p, 0o644) },
	}
	for name, open := range openers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fifo := filepath.Join(t.TempDir(), "planted")
			if err := syscall.Mkfifo(fifo, 0o666); err != nil {
				t.Fatal(err)
			}
			// No reader: must fail immediately rather than hang the restore.
			err := callWithTimeout(t, func() (*os.File, error) { return open(fifo) })
			if err == nil || !strings.Contains(err.Error(), "non-regular") {
				t.Fatalf("no reader: err = %v", err)
			}
			// A waiting reader: must not receive the restored data.
			r, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			err = callWithTimeout(t, func() (*os.File, error) { return open(fifo) })
			if err == nil || !strings.Contains(err.Error(), "non-regular") {
				t.Fatalf("with reader: err = %v", err)
			}
		})
	}
}

func TestRestoreOpenTruncatesRegularFileAndRefusesSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("old contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, open := range []func(string) (*os.File, error){
		func(p string) (*os.File, error) { return openRestoreDestination(p, p, 0o644) },
	} {
		if err := os.WriteFile(path, []byte("old contents"), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := open(path)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		if info, _ := os.Stat(path); info.Size() != 0 {
			t.Fatalf("not truncated: %d bytes", info.Size())
		}
	}
	missing := filepath.Join(dir, "no-such-dir", "f")
	if f, err := openRestoreDestination(missing, missing, 0o644); err == nil {
		_ = f.Close()
		t.Fatal("opened a path with a missing parent")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if f, err := openRestoreDestination(link, link, 0o644); err == nil {
		_ = f.Close()
		t.Fatal("followed a symlink")
	}
}

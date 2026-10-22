//go:build !windows

package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #514: an empty local path resolved against the daemon's working
// directory. Every operation on an adapter with an unsafe root must fail
// before it touches the filesystem.
func TestLocalAdapterUnsafeRootFailsClosed514(t *testing.T) {
	// Not parallel: chdir makes a relative root point at a temp dir, so a
	// regression would write there rather than into the package directory.
	work := t.TempDir()
	t.Chdir(work)
	if err := os.WriteFile(filepath.Join(work, "victim.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "languages"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{"", ".", "relative", "/", "/usr/local/emhttp", "/mnt/user"} {
		a := NewLocalAdapter(root)
		ops := map[string]func() error{
			"Write":  func() error { return a.Write("languages/new.txt", strings.NewReader("x")) },
			"Read":   func() error { _, err := a.Read("victim.txt"); return err },
			"Range":  func() error { _, err := a.ReadRange("victim.txt", 0, 1); return err },
			"Delete": func() error { return a.Delete("victim.txt") },
			"List":   func() error { _, err := a.List("."); return err },
			"Stat":   func() error { _, err := a.Stat("victim.txt"); return err },
			"RmDir":  func() error { return a.RemoveEmptyDir("languages") },
			"Test":   a.TestConnection,
			"Cap":    func() error { _, err := a.GetCapacity(context.Background()); return err },
			"WriteFrom": func() error {
				return a.WriteFrom("languages/x", func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil })
			},
		}
		for name, op := range ops {
			if err := op(); err == nil {
				t.Errorf("root %q: %s succeeded on an unsafe root", root, name)
			}
		}
		if _, err := NewValidatedLocalAdapter(root); err == nil {
			t.Errorf("NewValidatedLocalAdapter(%q) accepted an unsafe root", root)
		}
	}
	if b, err := os.ReadFile(filepath.Join(work, "victim.txt")); err != nil || string(b) != "keep" {
		t.Fatalf("working directory content changed: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(work, "languages")); err != nil {
		t.Fatalf("working directory folder removed: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(work, "languages"))
	if len(entries) != 0 {
		t.Fatalf("files written into the working directory: %v", entries)
	}
}

func TestFactoryRejectsUnsafeLocalRoot514(t *testing.T) {
	t.Parallel()
	for _, cfg := range []string{`{}`, `{"path":""}`, `{"path":"/"}`, `{"path":"backups"}`, `{"path":"/usr/local/emhttp"}`} {
		if a, err := NewAdapter("local", cfg); err == nil {
			CloseAdapter(a)
			t.Errorf("NewAdapter(local, %s) accepted an unsafe root", cfg)
		}
	}
	a, err := NewAdapter("local", `{"path":"`+t.TempDir()+`"}`)
	if err != nil {
		t.Fatalf("valid root rejected: %v", err)
	}
	CloseAdapter(a)
}

// The connection probe must never overwrite an existing file and must leave
// nothing behind.
func TestLocalTestConnectionProbeIsExclusive514(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	existing := filepath.Join(dir, ".vault_test")
	if err := os.WriteFile(existing, []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewLocalAdapter(dir).TestConnection(); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if b, err := os.ReadFile(existing); err != nil || string(b) != "user data" {
		t.Fatalf(".vault_test was overwritten or removed: %q, %v", b, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("probe left files behind: %v", entries)
	}
}

// A base that cannot be resolved for a reason other than "missing" fails
// closed instead of skipping the symlink containment check.
func TestVerifyNoSymlinkEscapeFailsClosed514(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	loop := filepath.Join(base, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := verifyNoSymlinkEscape(loop, filepath.Join(loop, "x")); err == nil {
		t.Fatal("unresolvable base passed the containment check")
	}
	if err := verifyNoSymlinkEscape(filepath.Join(base, "missing"), filepath.Join(base, "missing", "x")); err != nil {
		t.Fatalf("missing base should defer to the operation: %v", err)
	}
}

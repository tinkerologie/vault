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

// The write probe reports every failure: creating it, writing or closing it,
// and removing it afterwards (a probe left behind would litter the
// destination). Not parallel: it swaps package-level seams.
func TestLocalTestConnectionProbeFailures514(t *testing.T) {
	dir := t.TempDir()
	origCreate, origRemove := createWriteProbe, removeWriteProbe
	t.Cleanup(func() { createWriteProbe, removeWriteProbe = origCreate, origRemove })

	cases := map[string]struct {
		create func(string, string) (*os.File, error)
		remove func(string) error
		want   string
	}{
		"create fails": {
			create: func(string, string) (*os.File, error) { return nil, os.ErrPermission },
			want:   "not writable",
		},
		"write and close fail": {
			// A file that is already closed rejects both the write and the close.
			create: func(d, p string) (*os.File, error) {
				f, err := os.CreateTemp(d, p)
				if err == nil {
					_ = f.Close()
				}
				return f, err
			},
			want: "not writable",
		},
		"remove fails": {
			remove: func(string) error { return os.ErrPermission },
			want:   "remove write probe",
		},
	}
	for name, tc := range cases {
		createWriteProbe, removeWriteProbe = origCreate, origRemove
		if tc.create != nil {
			createWriteProbe = tc.create
		}
		if tc.remove != nil {
			removeWriteProbe = tc.remove
		}
		if err := NewLocalAdapter(dir).TestConnection(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: TestConnection = %v, want error containing %q", name, err, tc.want)
		}
	}
}

// The NFS adapter refuses to mount under a protected temp directory, before
// it creates or mounts anything. Not parallel: it sets TMPDIR.
func TestNFSMountRejectsProtectedTempDir514(t *testing.T) {
	t.Setenv("TMPDIR", "/usr/local/emhttp")
	n, err := NewNFSAdapter(NFSConfig{Host: "nas.invalid", Export: "/export", Version: "4"})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.mount(); err == nil || !strings.Contains(err.Error(), "unsafe mount location") {
		t.Fatalf("mount = %v, want unsafe mount location", err)
	}
	if n.mounted || n.mountDir != "" {
		t.Fatal("adapter marked mounted after a refused mount")
	}
}

// A base path that names a symlink inside the export to a protected host
// directory is refused, and the export is unmounted again. The fake mount
// only plants the symlink: nothing is written through it. Not parallel: it
// swaps the mount seams.
func TestNFSMountRejectsSymlinkedBasePath514(t *testing.T) {
	origMount, origUnmount := mountExport, unmountExport
	t.Cleanup(func() { mountExport, unmountExport = origMount, origUnmount })
	t.Setenv("TMPDIR", t.TempDir())
	unmounted := ""
	mountExport = func(args ...string) ([]byte, error) {
		target := args[len(args)-1]
		return nil, os.Symlink("/usr", filepath.Join(target, "link"))
	}
	unmountExport = func(dir string) error { unmounted = dir; return nil }

	n, err := NewNFSAdapter(NFSConfig{Host: "nas.invalid", Export: "/export", Version: "4", BasePath: "link/local/emhttp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.mount(); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("mount = %v, want a protected-path refusal", err)
	}
	if n.mounted || unmounted == "" {
		t.Fatalf("mounted=%v unmounted=%q; want the export unmounted", n.mounted, unmounted)
	}

	// A safe base path inside the export mounts normally.
	mountExport = func(args ...string) ([]byte, error) {
		return nil, os.Mkdir(filepath.Join(args[len(args)-1], "vault"), 0o755)
	}
	ok, _ := NewNFSAdapter(NFSConfig{Host: "nas.invalid", Export: "/export", Version: "4", BasePath: "vault"})
	if err := ok.mount(); err != nil || !ok.mounted {
		t.Fatalf("safe mount = %v (mounted %v)", err, ok.mounted)
	}
	ok.unmount()
}

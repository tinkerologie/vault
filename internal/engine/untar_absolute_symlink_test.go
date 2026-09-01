package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type tarEntry struct {
	header *tar.Header
	body   []byte
}

// writeTar writes the given entries to a temp archive.
func writeTar(t *testing.T, entries ...tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		if err := tw.WriteHeader(e.header); err != nil {
			t.Fatal(err)
		}
		if len(e.body) > 0 {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "vol.tar")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A container volume routinely holds symlinks whose target is absolute in the
// CONTAINER's namespace. Rejecting them aborted the entire restore, which is
// why a full classic restore of the mysql image failed.
func TestUntarRestoresAbsoluteSymlink(t *testing.T) {
	body := []byte("x")
	archive := writeTar(t,
		tarEntry{header: &tar.Header{Typeflag: tar.TypeDir, Name: "conf", Mode: 0o755}},
		tarEntry{header: &tar.Header{Typeflag: tar.TypeReg, Name: "conf/real.cnf", Size: int64(len(body)), Mode: 0o644}, body: body},
		tarEntry{header: &tar.Header{Typeflag: tar.TypeSymlink, Name: "conf/my.cnf", Linkname: "/etc/mysql/my.cnf"}},
		tarEntry{header: &tar.Header{Typeflag: tar.TypeSymlink, Name: "conf/rel.cnf", Linkname: "real.cnf"}},
	)
	dest := t.TempDir()

	if err := untarDirectory(context.Background(), archive, dest); err != nil {
		t.Fatalf("untarDirectory() with an absolute symlink: %v", err)
	}

	got, err := os.Readlink(filepath.Join(dest, "conf", "my.cnf"))
	if err != nil {
		t.Fatalf("absolute symlink was not created: %v", err)
	}
	if got != "/etc/mysql/my.cnf" {
		t.Fatalf("link target = %q, want it recreated verbatim as /etc/mysql/my.cnf", got)
	}
	if got, err := os.Readlink(filepath.Join(dest, "conf", "rel.cnf")); err != nil || got != "real.cnf" {
		t.Fatalf("relative symlink = %q, %v; want real.cnf", got, err)
	}
}

// A link that fails validation is skipped with a log line, not fatal: the rest
// of the archive must still be restored. Refusing to create it is the entire
// safety property; aborting only adds data loss. This case is reachable in
// practice now that absolute links are permitted — a relative link pointing at
// one resolves outside the destination.
func TestUntarSkipsUnsafeSymlinkAndContinues(t *testing.T) {
	outside := t.TempDir()
	body := []byte("kept")
	archive := writeTar(t,
		tarEntry{header: &tar.Header{Typeflag: tar.TypeSymlink, Name: "abs", Linkname: outside}},
		tarEntry{header: &tar.Header{Typeflag: tar.TypeSymlink, Name: "via-abs", Linkname: "abs"}},
		tarEntry{header: &tar.Header{Typeflag: tar.TypeReg, Name: "after.txt", Size: int64(len(body)), Mode: 0o644}, body: body},
	)
	dest := t.TempDir()

	if err := untarDirectory(context.Background(), archive, dest); err != nil {
		t.Fatalf("untarDirectory() should skip the unsafe link, not fail: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "via-abs")); !os.IsNotExist(err) {
		t.Fatal("the unsafe symlink should not have been created")
	}
	// The entry after it still has to be restored.
	got, err := os.ReadFile(filepath.Join(dest, "after.txt")) // #nosec G304 — test-local path
	if err != nil || string(got) != "kept" {
		t.Fatalf("entries after an unsafe symlink were not restored: %q, %v", got, err)
	}
}

// Accepting the link must not mean accepting writes through it: an entry
// addressed via an absolute symlink still has to be refused, and nothing may
// be created outside the destination.
func TestUntarRejectsEntryThroughAbsoluteSymlink(t *testing.T) {
	outside := t.TempDir()
	body := []byte("pwned")
	archive := writeTar(t,
		tarEntry{header: &tar.Header{Typeflag: tar.TypeSymlink, Name: "escape", Linkname: outside}},
		tarEntry{header: &tar.Header{Typeflag: tar.TypeReg, Name: "escape/evil.txt", Size: int64(len(body)), Mode: 0o644}, body: body},
	)
	dest := t.TempDir()

	if err := untarDirectory(context.Background(), archive, dest); err == nil {
		t.Fatal("untarDirectory() should reject an entry written through an absolute symlink")
	}
	if _, err := os.Lstat(filepath.Join(outside, "evil.txt")); !os.IsNotExist(err) {
		t.Fatalf("entry escaped the destination into %s", outside)
	}
}

// Symlinks are archived with tar.FileInfoHeader so they carry uid/gid like
// every other entry; a hand-built header's zero Uid/Gid would make restore
// Lchown every link to root:root.
func TestTarDirectoryRecordsSymlinkOwner(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(src, "link"))
	if err != nil {
		t.Fatal(err)
	}
	wantUID, wantGID := fileOwner(info)
	if wantUID < 0 || wantGID < 0 {
		t.Skip("ownership not available on this platform")
	}

	archive := filepath.Join(t.TempDir(), "out.tar")
	if err := tarDirectory(context.Background(), src, archive, nil, "none"); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(archive) // #nosec G304 — test-local path
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var seen bool
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Typeflag != tar.TypeSymlink {
			continue
		}
		seen = true
		if h.Linkname != "real.txt" {
			t.Fatalf("Linkname = %q, want real.txt", h.Linkname)
		}
		if h.Uid != wantUID || h.Gid != wantGID {
			t.Fatalf("symlink header owner = %d:%d, want %d:%d", h.Uid, h.Gid, wantUID, wantGID)
		}
	}
	if !seen {
		t.Fatal("no symlink entry in archive")
	}
}

// The incremental/differential walker archives symlinks too, and must record
// their owner the same way the full walker does.
func TestTarDirectoryFilteredRecordsSymlinkOwner(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/absolute/target", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(src, "link"))
	if err != nil {
		t.Fatal(err)
	}
	wantUID, wantGID := fileOwner(info)
	if wantUID < 0 || wantGID < 0 {
		t.Skip("ownership not available on this platform")
	}

	archive := filepath.Join(t.TempDir(), "out.tar")
	// Zero changedSince and no prevPaths: everything is considered changed.
	if err := tarDirectoryFilteredWithPrev(context.Background(), src, archive, time.Time{}, nil, "none", nil); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(archive) // #nosec G304 — test-local path
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var seen bool
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Typeflag != tar.TypeSymlink {
			continue
		}
		seen = true
		if h.Linkname != "/etc/absolute/target" {
			t.Fatalf("Linkname = %q, want /etc/absolute/target", h.Linkname)
		}
		if h.Uid != wantUID || h.Gid != wantGID {
			t.Fatalf("symlink header owner = %d:%d, want %d:%d", h.Uid, h.Gid, wantUID, wantGID)
		}
	}
	if !seen {
		t.Fatal("no symlink entry in archive")
	}
}

// removeExistingNonDir deliberately leaves directories alone, so a symlink
// entry landing on an existing directory surfaces the os.Symlink error rather
// than silently deleting the directory's contents.
func TestUntarSymlinkOverExistingDirectoryFails(t *testing.T) {
	archive := writeTar(t,
		tarEntry{header: &tar.Header{Typeflag: tar.TypeSymlink, Name: "occupied", Linkname: "elsewhere"}},
	)
	dest := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dest, "occupied", "child"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := untarDirectory(context.Background(), archive, dest); err == nil {
		t.Fatal("untarDirectory() should fail rather than replace an existing directory with a symlink")
	}
	// The directory and its contents must survive.
	if _, err := os.Stat(filepath.Join(dest, "occupied", "child")); err != nil {
		t.Fatalf("existing directory was destroyed: %v", err)
	}
}

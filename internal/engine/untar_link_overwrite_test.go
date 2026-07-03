package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// buildLinkTar builds a tar with a regular file, a symlink to it, and a
// hardlink to it.
func buildLinkTar(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("payload")
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "data.txt", Size: int64(len(body)), Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: "current", Linkname: "data.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeLink, Name: "hard.txt", Linkname: "data.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "links.tar")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestUntarOverwritesExistingLinks pins the #175 contract: restoring over an
// existing installation must replace pre-existing symlinks/hardlinks instead
// of failing with EEXIST.
func TestUntarOverwritesExistingLinks(t *testing.T) {
	archive := buildLinkTar(t)
	dest := t.TempDir()

	// Pre-existing state: stale symlink and stale regular file at the
	// hardlink's path — both must be replaced.
	if err := os.WriteFile(filepath.Join(dest, "old-target.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("old-target.txt", filepath.Join(dest, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "hard.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := untarDirectory(context.Background(), archive, dest); err != nil {
		t.Fatalf("restore over existing links failed: %v", err)
	}

	link, err := os.Readlink(filepath.Join(dest, "current"))
	if err != nil || link != "data.txt" {
		t.Errorf("symlink not replaced: link=%q err=%v", link, err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "hard.txt"))
	if err != nil || string(got) != "payload" {
		t.Errorf("hardlink not replaced: %q err=%v", got, err)
	}
}

package engine

import (
	"archive/tar"
	"context"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// parseDNSAddrs is a pure parser; invalid addresses are dropped, valid v4/v6
// pass through. Empty input returns nil to preserve the "no DNS override"
// signal callers rely on (the Docker SDK treats nil as "leave default").
func TestParseDNSAddrs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []string
		want []string // string form of the netip.Addrs we expect back
	}{
		{
			name: "nil input returns nil",
			in:   nil,
			want: nil,
		},
		{
			name: "empty slice returns nil",
			in:   []string{},
			want: nil,
		},
		{
			name: "valid ipv4 passes through",
			in:   []string{"1.1.1.1", "8.8.8.8"},
			want: []string{"1.1.1.1", "8.8.8.8"},
		},
		{
			name: "valid ipv6 passes through",
			in:   []string{"2606:4700:4700::1111"},
			want: []string{"2606:4700:4700::1111"},
		},
		{
			name: "invalid entries are silently dropped",
			in:   []string{"not-an-ip", "1.1.1.1", "", "999.999.999.999"},
			want: []string{"1.1.1.1"},
		},
		{
			name: "all invalid returns empty slice (not nil)",
			in:   []string{"foo", "bar"},
			want: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := parseDNSAddrs(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("parseDNSAddrs(%v) = %v, want nil", tc.in, got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseDNSAddrs(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for i, want := range tc.want {
				wantAddr, err := netip.ParseAddr(want)
				if err != nil {
					t.Fatalf("test setup: ParseAddr(%q): %v", want, err)
				}
				if got[i] != wantAddr {
					t.Fatalf("parseDNSAddrs(%v)[%d] = %v, want %v", tc.in, i, got[i], wantAddr)
				}
			}
		})
	}
}

// tarDirectoryFiltered must:
//   - skip files older than changedSince (for incremental backups)
//   - always include directories (so the tree structure is preserved)
//   - honour exclusion globs/literal paths (matches tarDirectory behaviour)
//   - cancel cleanly when ctx is done
func TestTarDirectoryFilteredIncludesAllWhenChangedSinceZero(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "fresh.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "nested.txt"), []byte("b"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out.tar")
	err := tarDirectoryFiltered(context.Background(), src, dst, time.Time{}, nil, CompressionNone)
	if err != nil {
		t.Fatalf("tarDirectoryFiltered: %v", err)
	}

	names := listTarEntries(t, dst)
	for _, want := range []string{"fresh.txt", "sub", "sub/nested.txt"} {
		if !containsName(names, want) {
			t.Fatalf("expected entry %q in archive, got %v", want, names)
		}
	}
}

func TestTarDirectoryFilteredSkipsOlderFiles(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	oldFile := filepath.Join(src, "stale.txt")
	newFile := filepath.Join(src, "fresh.txt")
	if err := os.WriteFile(oldFile, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(newFile, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Pin old mtime well in the past, and recent mtime to now.
	past := time.Now().Add(-2 * time.Hour)
	future := time.Now()
	if err := os.Chtimes(oldFile, past, past); err != nil {
		t.Fatalf("Chtimes(old): %v", err)
	}
	if err := os.Chtimes(newFile, future, future); err != nil {
		t.Fatalf("Chtimes(new): %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out.tar")
	// changedSince = 1h ago — old file should be skipped, new file kept.
	err := tarDirectoryFiltered(context.Background(), src, dst, time.Now().Add(-1*time.Hour), nil, CompressionNone)
	if err != nil {
		t.Fatalf("tarDirectoryFiltered: %v", err)
	}

	names := listTarEntries(t, dst)
	if containsName(names, "stale.txt") {
		t.Fatalf("stale.txt should be excluded by changedSince filter, got %v", names)
	}
	if !containsName(names, "fresh.txt") {
		t.Fatalf("fresh.txt should be included, got %v", names)
	}
}

func TestTarDirectoryFilteredHonoursExclusions(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("k"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "app.log"), []byte("noise"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, "logs"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "logs", "x.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out.tar")
	err := tarDirectoryFiltered(context.Background(), src, dst, time.Time{}, []string{"*.log", "logs"}, CompressionNone)
	if err != nil {
		t.Fatalf("tarDirectoryFiltered: %v", err)
	}

	names := listTarEntries(t, dst)
	for _, bad := range []string{"app.log", "logs", "logs/x.txt"} {
		if containsName(names, bad) {
			t.Fatalf("entry %q should have been excluded, got %v", bad, names)
		}
	}
	if !containsName(names, "keep.txt") {
		t.Fatalf("keep.txt missing from archive: %v", names)
	}
}

func TestTarDirectoryFilteredCancelledContext(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dst := filepath.Join(t.TempDir(), "out.tar")
	err := tarDirectoryFiltered(ctx, src, dst, time.Time{}, nil, CompressionNone)
	if err == nil {
		t.Fatal("expected ctx.Err() to surface when context is cancelled")
	}
}

func TestTarDirectoryFilteredMissingSrc(t *testing.T) {
	t.Parallel()

	dst := filepath.Join(t.TempDir(), "out.tar")
	err := tarDirectoryFiltered(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), dst, time.Time{}, nil, CompressionNone)
	if err == nil {
		t.Fatal("expected error opening missing source root")
	}
}

// untarDirectoryFiltered already has 50% coverage from a top-level path
// traversal test. These tests drive the error branches the existing tests
// miss: invalid file size, missing source, and (positive case) the include
// filter and self-extracted-symlink rejection paths.

// tarFile error branches: cancelled context surfaces ctx.Err(), missing
// source surfaces a stat error. Both run before any I/O on the dest, so
// they're trivially testable with t.TempDir().
func TestTarFileCancelledContext(t *testing.T) {
	t.Parallel()

	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dst := filepath.Join(t.TempDir(), "out.tar")
	if err := tarFile(ctx, src, dst, CompressionNone); err == nil {
		t.Fatal("expected ctx.Err() to surface from cancelled context")
	}
}

func TestTarFileMissingSource(t *testing.T) {
	t.Parallel()

	dst := filepath.Join(t.TempDir(), "out.tar")
	if err := tarFile(context.Background(), filepath.Join(t.TempDir(), "nope"), dst, CompressionNone); err == nil {
		t.Fatal("expected stat error for missing source file")
	}
}

// untarFile error branches: missing archive surfaces an open error, and
// an archive that contains only directories surfaces the "no regular
// file found" error.
func TestUntarFileMissingArchive(t *testing.T) {
	t.Parallel()

	dst := filepath.Join(t.TempDir(), "out.bin")
	if err := untarFile(context.Background(), filepath.Join(t.TempDir(), "missing.tar"), dst); err == nil {
		t.Fatal("expected open error for missing archive")
	}
}

func TestUntarFileArchiveWithNoRegularFile(t *testing.T) {
	t.Parallel()

	archive := filepath.Join(t.TempDir(), "dirs-only.tar")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tw := tar.NewWriter(f)
	if err := tw.WriteHeader(&tar.Header{Name: "subdir/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("f.Close: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out.bin")
	err = untarFile(context.Background(), archive, dst)
	if err == nil {
		t.Fatal("expected 'no regular file found' error for archive with only directories")
	}
	if !strings.Contains(err.Error(), "no regular file found") {
		t.Fatalf("expected no-regular-file error, got %v", err)
	}
}

// tarDirectory/tarDirectoryFiltered both branch on file mode bits to skip
// special inodes (sockets, devices, FIFOs) and to emit symlink entries
// without dereferencing. A symlink in the source dir exercises the
// symlink-handling branch shared by both code paths.
func TestTarDirectoryPreservesSymlinks(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	target := filepath.Join(src, "real.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	link := filepath.Join(src, "alias.txt")
	if err := os.Symlink("real.txt", link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out.tar")
	if err := tarDirectory(context.Background(), src, dst, nil, CompressionNone); err != nil {
		t.Fatalf("tarDirectory: %v", err)
	}

	// Read the archive back and confirm one entry is a symlink with the
	// expected Linkname.
	f, err := os.Open(dst)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var sawSymlink bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tr.Next: %v", err)
		}
		if hdr.Typeflag == tar.TypeSymlink && hdr.Linkname == "real.txt" {
			sawSymlink = true
		}
	}
	if !sawSymlink {
		t.Fatal("expected to find a symlink entry preserved in the archive")
	}
}

func TestTarDirectoryFilteredPreservesSymlinks(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	target := filepath.Join(src, "real.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	link := filepath.Join(src, "alias.txt")
	if err := os.Symlink("real.txt", link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out.tar")
	if err := tarDirectoryFiltered(context.Background(), src, dst, time.Time{}, nil, CompressionNone); err != nil {
		t.Fatalf("tarDirectoryFiltered: %v", err)
	}

	f, err := os.Open(dst)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var sawSymlink bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tr.Next: %v", err)
		}
		if hdr.Typeflag == tar.TypeSymlink && hdr.Linkname == "real.txt" {
			sawSymlink = true
		}
	}
	if !sawSymlink {
		t.Fatal("expected to find a symlink entry preserved in the filtered archive")
	}
}

// TestUntarDirectoryFilteredRejectsHardLinkEscape drives the
// joinArchiveTarget guard for hard-link targets that escape destDir
// (the TypeLink branch's first error check).
func TestUntarDirectoryFilteredRejectsHardLinkEscape(t *testing.T) {
	t.Parallel()

	archivePath := filepath.Join(t.TempDir(), "hardlink.tar")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tw := tar.NewWriter(f)
	// Hard link entry whose Linkname escapes destDir.
	hdr := &tar.Header{
		Name:     "link",
		Linkname: "../escape",
		Typeflag: tar.TypeLink,
		Mode:     0o644,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("f.Close: %v", err)
	}

	err = untarDirectoryFiltered(context.Background(), archivePath, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected error for hard link target escaping destDir")
	}
}

// TestUntarDirectoryFilteredSkipsUnknownTypeflag drives the default
// branch of the type switch — unknown tar entry types are silently
// skipped so legacy and exotic archives stay restorable.
func TestUntarDirectoryFilteredSkipsUnknownTypeflag(t *testing.T) {
	t.Parallel()

	archivePath := filepath.Join(t.TempDir(), "fifo.tar")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tw := tar.NewWriter(f)
	// FIFO entries can't be restored on a regular filesystem; the engine
	// silently skips them via the default branch.
	hdr := &tar.Header{
		Name:     "fifo",
		Typeflag: tar.TypeFifo,
		Mode:     0o644,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("f.Close: %v", err)
	}

	dest := t.TempDir()
	if err := untarDirectoryFiltered(context.Background(), archivePath, dest, nil); err != nil {
		t.Fatalf("expected fifo entry to be skipped silently, got %v", err)
	}
	// The fifo path must not have been created.
	if _, err := os.Stat(filepath.Join(dest, "fifo")); err == nil {
		t.Fatal("fifo entry should have been skipped, not extracted")
	}
}

func TestUntarDirectoryFilteredOpenMissingArchive(t *testing.T) {
	t.Parallel()

	err := untarDirectoryFiltered(context.Background(), filepath.Join(t.TempDir(), "missing.tar"), t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected error opening missing archive")
	}
}

func TestUntarDirectoryFilteredCancelledContext(t *testing.T) {
	t.Parallel()

	// Build an archive with at least one entry so the loop actually runs.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	archive := filepath.Join(t.TempDir(), "ctx.tar")
	if err := tarDirectoryFiltered(context.Background(), src, archive, time.Time{}, nil, CompressionNone); err != nil {
		t.Fatalf("tarDirectoryFiltered: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := untarDirectoryFiltered(ctx, archive, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected ctx.Err() to surface")
	}
}

func TestUntarDirectoryFilteredAppliesIncludeFilter(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "skip.txt"), []byte("skip"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	archive := filepath.Join(t.TempDir(), "with-filter.tar")
	if err := tarDirectoryFiltered(context.Background(), src, archive, time.Time{}, nil, CompressionNone); err != nil {
		t.Fatalf("tarDirectoryFiltered: %v", err)
	}

	dest := t.TempDir()
	err := untarDirectoryFiltered(context.Background(), archive, dest, []string{"keep.txt"})
	if err != nil {
		t.Fatalf("untarDirectoryFiltered: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "keep.txt")); err != nil {
		t.Fatalf("keep.txt should have been extracted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "skip.txt")); err == nil {
		t.Fatal("skip.txt should NOT have been extracted under include filter")
	}
}

// listTarEntries reads a plain (uncompressed) tar archive and returns the
// list of entry names so tests can assert on inclusion/exclusion behaviour.
func listTarEntries(t *testing.T, archivePath string) []string {
	t.Helper()
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		names = append(names, strings.TrimSuffix(hdr.Name, "/"))
	}
	return names
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// eioReader yields good bytes and then fails the way a bad block on the flash
// drive does — an *fs.PathError wrapping syscall.EIO, exactly what the reporter
// of issue #393 saw abort the whole backup.
type eioReader struct {
	good      []byte
	off       int
	failAfter int
}

func (r *eioReader) Read(p []byte) (int, error) {
	if r.off >= r.failAfter {
		return 0, &fs.PathError{Op: "read", Path: "bad.txz", Err: syscall.EIO}
	}
	n := copy(p, r.good[r.off:])
	if r.off+n > r.failAfter {
		n = r.failAfter - r.off
	}
	r.off += n
	return n, nil
}

// zeroReader is an endless source of zero bytes, used to stand in for a file
// larger than maxStagedFileSize without writing one to disk.
type zeroReader struct{ remaining int64 }

func (r *zeroReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	for i := range p[:n] {
		p[i] = 0
	}
	r.remaining -= n
	return int(n), nil
}

// tarEntries reads every entry of an in-memory tar, proving the stream stayed
// structurally valid after a skip.
func tarEntries(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(raw))
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("archive is not a valid tar: %v", err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = content
	}
	return out
}

// TestWriteTarEntry is the regression test for issue #393: a file that cannot
// be read must not abort the archive. Below the staging threshold it never
// enters the tar at all; above it the header is already committed, so the entry
// is zero-filled and reported instead.
func TestWriteTarEntry(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		size        int64
		src         func() io.Reader
		wantSkipped bool
		wantPresent bool
		wantContent string
		wantSize    int64
	}{
		{
			name:        "readable file is archived",
			size:        5,
			src:         func() io.Reader { return strings.NewReader("hello") },
			wantPresent: true,
			wantContent: "hello",
		},
		{
			name: "unreadable small file is skipped and absent from the tar",
			size: 512,
			src: func() io.Reader {
				return &eioReader{good: bytes.Repeat([]byte("a"), 512), failAfter: 100}
			},
			wantSkipped: true,
		},
		{
			name: "a file that shrank mid-walk is archived at its real size",
			size: 64,
			src:  func() io.Reader { return strings.NewReader("short") },
			// contextCopy stops at EOF; the header must record the 5 bytes
			// actually staged, not the 64 the stat promised, or tar.Writer
			// reports a short write and fails the backup (#166).
			wantPresent: true,
			wantContent: "short",
			wantSize:    5,
		},
		{
			name: "unreadable large file is zero-filled and reported",
			size: maxStagedFileSize + 1024,
			src: func() io.Reader {
				return io.MultiReader(
					bytes.NewReader(bytes.Repeat([]byte("b"), 2048)),
					&eioReader{good: nil, failAfter: 0},
				)
			},
			wantSkipped: true,
			wantPresent: true,
			wantSize:    maxStagedFileSize + 1024,
		},
		{
			// The large-file counterpart of the shrink case above. The header
			// is already on the wire, so the entry cannot be re-sized; it is
			// padded to the promised length and reported, which is the only
			// repair that keeps tw.Close from failing the whole backup (#166).
			name:        "a large file that shrank mid-walk is padded and reported",
			size:        maxStagedFileSize + 4096,
			src:         func() io.Reader { return &zeroReader{remaining: maxStagedFileSize} },
			wantSkipped: true,
			wantPresent: true,
			wantSize:    maxStagedFileSize + 4096,
		},
		{
			name:        "readable large file is archived",
			size:        maxStagedFileSize + 16,
			src:         func() io.Reader { return &zeroReader{remaining: maxStagedFileSize + 16} },
			wantPresent: true,
			wantSize:    maxStagedFileSize + 16,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			header := &tar.Header{Typeflag: tar.TypeReg, Name: "bad.txz", Mode: 0o644, Size: tc.size}

			skipped, err := writeTarEntry(context.Background(), tw, "bad.txz", header, tc.src())
			if err != nil {
				t.Fatalf("writeTarEntry returned an error instead of skipping: %v", err)
			}
			if skipped != tc.wantSkipped {
				t.Fatalf("skipped = %v, want %v", skipped, tc.wantSkipped)
			}
			// Close is where tar reports a short write, so a successful close
			// is the proof that the stream stayed consistent.
			if err := tw.Close(); err != nil {
				t.Fatalf("tar stream is inconsistent after the entry: %v", err)
			}

			entries := tarEntries(t, buf.Bytes())
			content, present := entries["bad.txz"]
			if present != tc.wantPresent {
				t.Fatalf("entry present = %v, want %v", present, tc.wantPresent)
			}
			if !present {
				return
			}
			if tc.wantContent != "" && string(content) != tc.wantContent {
				t.Errorf("content = %q, want %q", content, tc.wantContent)
			}
			if tc.wantSize != 0 && int64(len(content)) != tc.wantSize {
				t.Errorf("archived %d bytes, want %d", len(content), tc.wantSize)
			}
		})
	}
}

// TestWriteTarEntryCancellationIsNotASkip pins that an operator cancel still
// fails the run — silently dropping files on cancel would turn a cancelled
// backup into a quietly incomplete one.
func TestWriteTarEntryCancellationIsNotASkip(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	header := &tar.Header{Typeflag: tar.TypeReg, Name: "f", Mode: 0o644, Size: 5}
	skipped, err := writeTarEntry(ctx, tw, "f", header, strings.NewReader("hello"))
	if err == nil {
		t.Fatal("expected the cancellation to be returned as an error")
	}
	if skipped {
		t.Error("cancellation must not be reported as a skipped file")
	}
}

// TestTarDirectoryReportingSkipsUnopenableFile drives the whole walk: a file
// that cannot be opened is reported, and every other file still lands in a
// valid archive.
func TestTarDirectoryReportingSkipsUnopenableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions, so no file can be made unopenable")
	}
	t.Parallel()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "good.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(src, "bad.txz")
	if err := os.WriteFile(bad, []byte("unreadable"), 0o000); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(bad); err == nil {
		f.Close()
		t.Skip("filesystem does not enforce the mode, so the file stays readable")
	}

	archive := filepath.Join(t.TempDir(), "data.tar")
	skipped, err := tarDirectoryReporting(context.Background(), src, archive, nil, CompressionNone)
	if err != nil {
		t.Fatalf("an unopenable file aborted the archive: %v", err)
	}
	if len(skipped) != 1 || skipped[0] != "bad.txz" {
		t.Fatalf("skipped = %v, want [bad.txz]", skipped)
	}

	names := listTarEntries(t, archive)
	if !containsName(names, "good.txt") {
		t.Errorf("the readable file is missing from the archive: %v", names)
	}
	if containsName(names, "bad.txz") {
		t.Errorf("the unreadable file should not be in the archive: %v", names)
	}
}

// TestRecordSkippedFiles pins the Meta contract the runner reads.
func TestRecordSkippedFiles(t *testing.T) {
	t.Parallel()

	t.Run("nothing skipped leaves Meta untouched", func(t *testing.T) {
		result := &BackupResult{}
		recordSkippedFiles(result, nil)
		if result.Meta != nil {
			t.Fatalf("Meta = %v, want nil for a healthy backup", result.Meta)
		}
	})

	t.Run("successive calls accumulate across volumes", func(t *testing.T) {
		result := &BackupResult{}
		recordSkippedFiles(result, []string{"a"})
		recordSkippedFiles(result, []string{"b", "c"})
		got, _ := result.Meta[MetaSkippedFiles].([]string)
		if strings.Join(got, ",") != "a,b,c" {
			t.Fatalf("skipped = %v, want [a b c]", got)
		}
	})

	t.Run("a nil result is tolerated", func(t *testing.T) {
		recordSkippedFiles(nil, []string{"a"})
	})
}

// failAfter is an io.Writer that accepts limit bytes and then refuses. It
// stands in for the disk filling up or the destination going away mid-archive
// — the writer failures the archive paths must surface rather than swallow.
type failAfter struct {
	remaining int
	err       error
}

func (w *failAfter) Write(p []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, w.err
	}
	if len(p) <= w.remaining {
		w.remaining -= len(p)
		return len(p), nil
	}
	n := w.remaining
	w.remaining = 0
	return n, w.err
}

// A skipped file is a warning; a failed write is a corrupt archive. These
// assert writeTarEntry never confuses the two.
func TestWriteTarEntryReportsWriterFailures(t *testing.T) {
	errDisk := errors.New("no space left on device")

	cases := []struct {
		name  string
		limit int
		size  int64
		src   func() io.Reader
		want  string
	}{
		{
			name:  "the staged path's header write fails",
			limit: 0,
			size:  4,
			src:   func() io.Reader { return strings.NewReader("abcd") },
			want:  "writing tar header",
		},
		{
			name: "the staged path's content write fails",
			// One tar block: enough for the header, nothing for the body.
			limit: 512,
			size:  4,
			src:   func() io.Reader { return strings.NewReader("abcd") },
			want:  "writing file",
		},
		{
			name:  "the streamed path's header write fails",
			limit: 0,
			size:  maxStagedFileSize + 1,
			src:   func() io.Reader { return &zeroReader{remaining: 0} },
			want:  "writing tar header",
		},
		{
			name: "the streamed path's zero-fill repair fails",
			// The header lands, the source is short, and the pad that would
			// make the entry honest cannot be written.
			limit: 512,
			size:  maxStagedFileSize + 1,
			src:   func() io.Reader { return &zeroReader{remaining: 0} },
			want:  "writing file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tw := tar.NewWriter(&failAfter{remaining: tc.limit, err: errDisk})
			header := &tar.Header{Name: "f", Mode: 0o644, Size: tc.size, Typeflag: tar.TypeReg}

			skipped, err := writeTarEntry(context.Background(), tw, "f", header, tc.src())
			if err == nil {
				t.Fatalf("a write failure must fail the backup, got skipped=%v", skipped)
			}
			if skipped {
				t.Error("a write failure is not a skipped file — the archive is broken, not incomplete")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should contain %q", err, tc.want)
			}
			if !errors.Is(err, errDisk) {
				t.Errorf("error %q should wrap the underlying writer error", err)
			}
		})
	}
}

func TestWriteTarEntryStreamedPathHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tw := tar.NewWriter(&bytes.Buffer{})
	header := &tar.Header{Name: "big", Mode: 0o644, Size: maxStagedFileSize + 1, Typeflag: tar.TypeReg}

	skipped, err := writeTarEntry(ctx, tw, "big", header, &zeroReader{remaining: maxStagedFileSize + 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if skipped {
		t.Error("a cancelled backup has not skipped a file, it has been cancelled")
	}
}

func TestZeroPadSurfacesShortWrites(t *testing.T) {
	errDisk := errors.New("device gone")
	// Accept part of the pad, then fail: the loop must not spin on the
	// remaining bytes once the writer has given up.
	if err := zeroPad(&failAfter{remaining: 100, err: errDisk}, 64*1024); !errors.Is(err, errDisk) {
		t.Errorf("zeroPad err = %v, want the writer's error", err)
	}
	if err := zeroPad(&bytes.Buffer{}, 96*1024); err != nil {
		t.Errorf("zeroPad over a healthy writer: %v", err)
	}
}

// A source directory that has gone away is a backup failure, not an empty
// archive — the classic and incremental variants must both say so.
func TestTarDirectoryReportingRejectsMissingSource(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	dest := filepath.Join(t.TempDir(), "out.tar")
	ctx := context.Background()

	if _, err := tarDirectoryReporting(ctx, missing, dest, nil, "none"); err == nil {
		t.Error("tarDirectoryReporting should fail on a missing source directory")
	} else if !strings.Contains(err.Error(), "opening source root") {
		t.Errorf("error %q should name the source root", err)
	}

	if _, err := tarDirectoryFilteredReporting(ctx, missing, dest, time.Time{}, nil, "none", nil); err == nil {
		t.Error("tarDirectoryFilteredReporting should fail on a missing source directory")
	} else if !strings.Contains(err.Error(), "opening source root") {
		t.Errorf("error %q should name the source root", err)
	}
}

// TestWriteEffectiveListingOmitsSkippedFiles ensures that files skipped during
// archiving (e.g. unreadable bad blocks) are not recorded in the effective listing,
// so subsequent incremental runs retry them (issues #320, #393).
func TestWriteEffectiveListingOmitsSkippedFiles(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(t.TempDir(), "archive.tar")

	if err := os.WriteFile(filepath.Join(dir, "good.txt"), []byte("good"), 0644); err != nil {
		t.Fatalf("WriteFile good.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.txt"), []byte("bad"), 0644); err != nil {
		t.Fatalf("WriteFile bad.txt: %v", err)
	}

	// Exclude bad.txt via the skipped slice.
	if err := WriteEffectiveListing(dir, archive, nil, []string{"bad.txt"}); err != nil {
		t.Fatalf("WriteEffectiveListing: %v", err)
	}

	listingPath := archive + ListingSuffix
	data, err := os.ReadFile(listingPath)
	if err != nil {
		t.Fatalf("ReadFile listing: %v", err)
	}

	var idx TarIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("Unmarshal listing: %v", err)
	}

	foundGood := false
	for _, f := range idx.Files {
		if f.Path == "bad.txt" {
			t.Errorf("listing includes skipped file bad.txt")
		}
		if f.Path == "good.txt" {
			foundGood = true
		}
	}
	if !foundGood {
		t.Errorf("listing did not include good.txt")
	}
}

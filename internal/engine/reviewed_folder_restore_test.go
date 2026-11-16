package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/dedup"
)

func TestReviewedChunkedFolderStopsBeforeWritingNextChunk(t *testing.T) {
	configured := t.TempDir()
	resolved, err := ResolveFolderRestoreTarget(configured, true)
	if err != nil {
		t.Fatal(err)
	}
	target := ReviewedFolderTarget{Configured: configured, Resolved: resolved, Original: true}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, second, third := dedup.ID{1}, dedup.ID{2}, dedup.ID{3}
	manifest := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		"restored.txt": {Size: 3, Mode: 0o600, Chunks: []dedup.ID{first, second, third}},
	}}
	get := func(id dedup.ID) ([]byte, error) {
		if id == third {
			t.Error("read another chunk after cancellation")
		}
		if id == second {
			cancel()
		}
		return []byte{'a' + id[0] - 1}, nil
	}
	folder := &FolderHandler{}
	err = folder.RestorePreparedChunked(ctx, BackupItem{Name: "archive"}, target, manifest, get, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("restore error = %v, want cancellation", err)
	}
	data, err := os.ReadFile(filepath.Join(configured, "restored.txt"))
	if err != nil || string(data) != "a" {
		t.Fatalf("restore wrote beyond the first chunk: %q %v", data, err)
	}
}

func TestReviewedFolderPreflightRejectsOversizedArchiveEntry(t *testing.T) {
	staging := t.TempDir()
	file, err := os.OpenFile(filepath.Join(staging, "data.tar"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// A header is enough: preflight must reject the declared size before
	// reading the body or proceeding to replacement of the target.
	archive := tar.NewWriter(file)
	header := &tar.Header{Name: "fixture.bin", Typeflag: tar.TypeReg, Mode: 0o600, Size: maxExtractSize + 1}
	if err := archive.WriteHeader(header); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStagedFolder(t.Context(), staging, t.TempDir()); err == nil || !strings.Contains(err.Error(), "exceeds max extract size") {
		t.Fatalf("expected the declared size to be rejected before reading the entry body, got %v", err)
	}
}

func TestReviewedFolderPreflightStopsReadingWhenCancelled(t *testing.T) {
	const remaining = 8 << 20
	for _, part := range []string{"entry", "trailer"} {
		t.Run(part, func(t *testing.T) {
			var header bytes.Buffer
			archive := tar.NewWriter(&header)
			if part == "entry" {
				if err := archive.WriteHeader(&tar.Header{Name: "fixture.bin", Mode: 0o600, Size: remaining}); err != nil {
					t.Fatal(err)
				}
			} else if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			body := &cancellingArchiveBody{cancel: cancel, left: remaining}
			err := validateStagedFolderTar(ctx, io.MultiReader(&header, body), t.TempDir())
			if !errors.Is(err, context.Canceled) || body.left == 0 {
				t.Fatalf("preflight should stop before draining the cancelled %s: unread=%d, err=%v", part, body.left, err)
			}
		})
	}
}

func TestReviewedFolderPreflightChecksHardLinkTarget(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		wantError    bool
	}{
		{name: "relative target", target: "fixture.txt"},
		{name: "empty target", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var staged bytes.Buffer
			archive := tar.NewWriter(&staged)
			if err := archive.WriteHeader(&tar.Header{Name: "fixture.txt", Typeflag: tar.TypeReg, Mode: 0o600}); err != nil {
				t.Fatal(err)
			}
			if err := archive.WriteHeader(&tar.Header{Name: "fixture-link", Typeflag: tar.TypeLink, Linkname: tc.target}); err != nil {
				t.Fatal(err)
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			if err := validateStagedFolderTar(t.Context(), &staged, t.TempDir()); (err != nil) != tc.wantError {
				t.Fatalf("archive preflight error = %v, want error %t", err, tc.wantError)
			}
		})
	}
}

type cancellingArchiveBody struct {
	cancel context.CancelFunc
	left   int
}

func (r *cancellingArchiveBody) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	r.cancel()
	n := min(len(p), r.left)
	clear(p[:n])
	r.left -= n
	return n, nil
}

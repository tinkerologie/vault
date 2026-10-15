package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type tarEnt458 struct {
	name     string
	typ      byte
	body     string
	linkname string
}

// tarBytes458 builds an uncompressed tar holding the given entries.
func tarBytes458(t *testing.T, entries ...tarEnt458) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Linkname: e.linkname, ModTime: time.Unix(1_700_000_000, 0)}
		if e.typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if e.typ == tar.TypeDir {
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// truncatedTar458 is a tar whose single file's data stops part-way.
func truncatedTar458(t *testing.T) []byte {
	t.Helper()
	full := tarBytes458(t, tarEnt458{name: "big.bin", typ: tar.TypeReg, body: strings.Repeat("z", 4096)})
	return full[:512+100]
}

func writeArchive458(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "vol.tar")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func cancelledCtx458(t *testing.T) context.Context {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

func TestTarFile458(t *testing.T) {
	t.Parallel()

	src := filepath.Join(t.TempDir(), "hook.sh")
	writeFile458(t, src, "#!/bin/sh\necho hi\n")

	tests := []struct {
		name    string
		ctx     func(t *testing.T) context.Context
		src     string
		dest    func(t *testing.T) string
		wantErr string
	}{
		{name: "cancelled", ctx: cancelledCtx458, src: src, wantErr: context.Canceled.Error()},
		{name: "missing source", src: filepath.Join(t.TempDir(), "nope"), wantErr: "stat source file"},
		{name: "unwritable destination", src: src, dest: func(t *testing.T) string { return filepath.Join(t.TempDir(), "no", "dir.tar") }, wantErr: "creating archive file"},
		{name: "zstd archive", src: src},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			if tt.ctx != nil {
				ctx = tt.ctx(t)
			}
			dest := filepath.Join(t.TempDir(), "out.tar.zst")
			if tt.dest != nil {
				dest = tt.dest(t)
			}
			err := tarFile(ctx, tt.src, dest, CompressionZstd)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("tarFile: %v", err)
			}
			restored := filepath.Join(t.TempDir(), "hook.sh")
			if err := untarFile(t.Context(), dest, restored); err != nil {
				t.Fatalf("untarFile: %v", err)
			}
			if got := readFile458(t, restored); got != "#!/bin/sh\necho hi\n" {
				t.Errorf("round trip = %q", got)
			}
		})
	}
}

func TestUntarFileErrors458(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		archive []byte // nil: archive file absent
		dest    string // overrides the default absolute temp target
		ctx     func(t *testing.T) context.Context
		wantErr string
	}{
		{name: "relative destination", archive: []byte{}, dest: "relative/file", wantErr: "suspicious path"},
		{name: "missing archive", wantErr: "opening archive"},
		{name: "corrupt gzip", archive: []byte{0x1f, 0x8b}, wantErr: "gzip"},
		{name: "no regular file", archive: tarBytes458(t, tarEnt458{name: "d/", typ: tar.TypeDir}), wantErr: "no regular file"},
		{name: "not a tar", archive: bytes.Repeat([]byte{0xff}, 1024), wantErr: "reading tar entry"},
		{name: "truncated data", archive: truncatedTar458(t), wantErr: "writing file"},
		{name: "cancelled", archive: tarBytes458(t, tarEnt458{name: "f", typ: tar.TypeReg, body: "x"}), ctx: cancelledCtx458, wantErr: context.Canceled.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := filepath.Join(t.TempDir(), "absent.tar")
			if tt.archive != nil {
				src = writeArchive458(t, tt.archive)
			}
			dest := filepath.Join(t.TempDir(), "out")
			if tt.dest != "" {
				dest = tt.dest
			}
			ctx := t.Context()
			if tt.ctx != nil {
				ctx = tt.ctx(t)
			}
			err := untarFile(ctx, src, dest)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestUntarDirectoryFilteredErrors458(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		archive []byte
		ctx     func(t *testing.T) context.Context
		wantErr string
	}{
		{name: "corrupt gzip", archive: []byte{0x1f, 0x8b, 0x00}, wantErr: "gzip"},
		{name: "truncated data", archive: truncatedTar458(t), wantErr: "writing file"},
		{name: "cancelled", archive: tarBytes458(t, tarEnt458{name: "f", typ: tar.TypeReg, body: "x"}), ctx: cancelledCtx458, wantErr: context.Canceled.Error()},
		{
			name:    "hard link to a missing file",
			archive: tarBytes458(t, tarEnt458{name: "link", typ: tar.TypeLink, linkname: "missing"}),
			wantErr: "creating hard link",
		},
		{
			name: "directory over an existing file",
			archive: tarBytes458(t,
				tarEnt458{name: "x", typ: tar.TypeReg, body: "file"},
				tarEnt458{name: "x/", typ: tar.TypeDir},
			),
			wantErr: "x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := writeArchive458(t, tt.archive)
			ctx := t.Context()
			if tt.ctx != nil {
				ctx = tt.ctx(t)
			}
			err := untarDirectoryFiltered(ctx, src, t.TempDir(), nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestTarDirectoryReportingErrors458(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	writeFile458(t, filepath.Join(src, "a.txt"), "a")
	old := time.Now().Add(-time.Hour)

	type archiver func(ctx context.Context, dest string) error
	full := func(ctx context.Context, dest string) error {
		_, err := tarDirectoryReporting(ctx, src, dest, nil, CompressionNone)
		return err
	}
	filtered := func(ctx context.Context, dest string) error {
		_, err := tarDirectoryFilteredReporting(ctx, src, dest, old, nil, CompressionNone, nil)
		return err
	}
	for name, run := range map[string]archiver{"full": full, "filtered": filtered} {
		t.Run(name+"/unwritable destination", func(t *testing.T) {
			t.Parallel()
			err := run(t.Context(), filepath.Join(t.TempDir(), "missing", "v.tar"))
			if err == nil || !strings.Contains(err.Error(), "creating archive file") {
				t.Fatalf("err = %v", err)
			}
		})
		t.Run(name+"/cancelled", func(t *testing.T) {
			t.Parallel()
			err := run(cancelledCtx458(t), filepath.Join(t.TempDir(), "v.tar"))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
		})
	}
}

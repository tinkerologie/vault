package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestTar produces a tar archive at path containing the supplied
// (name, content, mode) tuples. If compression is "gzip", the bytes are
// wrapped in gzip; otherwise the archive is plain tar. Used by the index
// tests to fabricate archives without depending on the full engine
// machinery.
func writeTestTar(t *testing.T, path string, compress bool, entries []struct {
	name string
	body string
	mode int64
}) {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:    e.name,
			Mode:    e.mode,
			Size:    int64(len(e.body)),
			ModTime: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatalf("Write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}

	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("os.Create: %v", err)
	}
	defer out.Close()

	if compress {
		gw := gzip.NewWriter(out)
		if _, err := gw.Write(raw.Bytes()); err != nil {
			t.Fatalf("gzip.Write: %v", err)
		}
		if err := gw.Close(); err != nil {
			t.Fatalf("gzip.Close: %v", err)
		}
		return
	}
	if _, err := out.Write(raw.Bytes()); err != nil {
		t.Fatalf("out.Write: %v", err)
	}
}

func TestWriteTarIndex_PlainTar(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "data.tar")
	writeTestTar(t, archive, false, []struct {
		name string
		body string
		mode int64
	}{
		{"config/app.yml", "hello", 0o644},
		{"data/secret.txt", "shh", 0o600},
	})

	if err := WriteTarIndex(archive); err != nil {
		t.Fatalf("WriteTarIndex: %v", err)
	}

	idxBytes, err := os.ReadFile(archive + IndexSuffix)
	if err != nil {
		t.Fatalf("read index sidecar: %v", err)
	}
	var idx TarIndex
	if err := json.Unmarshal(idxBytes, &idx); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	if idx.Version != tarIndexVersion {
		t.Errorf("version = %d, want %d", idx.Version, tarIndexVersion)
	}
	if idx.Archive != "data.tar" {
		t.Errorf("archive = %q, want %q", idx.Archive, "data.tar")
	}
	if len(idx.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(idx.Files))
	}
	if idx.Files[0].Path != "config/app.yml" || idx.Files[0].Size != 5 || idx.Files[0].Mode != "0644" {
		t.Errorf("entry 0 unexpected: %+v", idx.Files[0])
	}
	if idx.Files[1].Path != "data/secret.txt" || idx.Files[1].Size != 3 || idx.Files[1].Mode != "0600" {
		t.Errorf("entry 1 unexpected: %+v", idx.Files[1])
	}
}

func TestWriteTarIndex_GzipTar(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "data.tar.gz")
	writeTestTar(t, archive, true, []struct {
		name string
		body string
		mode int64
	}{
		{"a.txt", "alpha", 0o644},
	})

	if err := WriteTarIndex(archive); err != nil {
		t.Fatalf("WriteTarIndex (gzip): %v", err)
	}
	idxBytes, err := os.ReadFile(archive + IndexSuffix)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var idx TarIndex
	if err := json.Unmarshal(idxBytes, &idx); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(idx.Files) != 1 || idx.Files[0].Path != "a.txt" || idx.Files[0].Size != 5 {
		t.Fatalf("unexpected index entries: %+v", idx.Files)
	}
}

func TestReadTarIndex_VersionMismatch(t *testing.T) {
	bad := bytes.NewReader([]byte(`{"version": 99, "files": []}`))
	if _, err := ReadTarIndex(bad); err == nil {
		t.Error("expected error on unsupported version")
	}
}

func TestWriteTarIndex_EmptyTar(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "empty.tar")
	writeTestTar(t, archive, false, nil)

	if err := WriteTarIndex(archive); err != nil {
		t.Fatalf("WriteTarIndex: %v", err)
	}
	idxBytes, err := os.ReadFile(archive + IndexSuffix)
	if err != nil {
		t.Fatal(err)
	}
	var idx TarIndex
	if err := json.Unmarshal(idxBytes, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Files) != 0 {
		t.Errorf("expected empty file list, got %d", len(idx.Files))
	}
}

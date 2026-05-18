package engine

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// TarIndex describes the contents of a tar archive (plain or compressed) for
// the partial-restore "Restore specific files…" feature. It is written as a
// JSON sidecar next to the archive at backup time and consumed by the API
// handler that powers the restore wizard's file picker.
type TarIndex struct {
	Version int             `json:"version"`
	Archive string          `json:"archive"` // basename of the archive this index describes
	Files   []TarIndexEntry `json:"files"`
}

// TarIndexEntry is one tar header's user-visible metadata. We deliberately
// avoid recording xattrs / link targets / device numbers etc — the file
// picker only needs path + size + mode + modtime to render meaningfully.
type TarIndexEntry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`    // octal string e.g. "0644"
	ModTime string `json:"modtime"` // RFC3339
	IsDir   bool   `json:"is_dir,omitempty"`
}

const tarIndexVersion = 1

// IndexSuffix is appended to an archive path to derive the sidecar filename:
// data.tar.zst -> data.tar.zst.index.json. The fixed suffix keeps the
// runner-side encryption pipeline simple: the index is just another regular
// file in the staging dir and gets uploaded (and encrypted if the job has
// encryption) automatically.
const IndexSuffix = ".index.json"

// WriteTarIndex opens the tar archive at archivePath (plain, gzip, or zstd),
// walks its entries, and writes a JSON index sidecar at
// `archivePath + IndexSuffix`. Returns an error if the archive cannot be
// read; existing index files are overwritten.
//
// This is best-effort from the engine's perspective — callers may log and
// ignore failures so a transient read error never fails a successful
// backup. The restore path falls back to whole-archive extract when no
// index is present.
func WriteTarIndex(archivePath string) error {
	f, err := os.Open(archivePath) // #nosec G304 — archivePath is engine-controlled tmpDir
	if err != nil {
		return fmt.Errorf("opening archive for indexing: %w", err)
	}
	defer f.Close()

	dr, closer, err := detectingReader(f)
	if err != nil {
		return fmt.Errorf("opening decompressor for indexing: %w", err)
	}
	defer func() { _ = closer() }()

	tr := tar.NewReader(dr)
	index := TarIndex{
		Version: tarIndexVersion,
		Archive: baseName(archivePath),
		Files:   make([]TarIndexEntry, 0, 64),
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar entry for indexing: %w", err)
		}
		// Skip global headers and tar-internal records (xattr/longname).
		switch hdr.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		}
		index.Files = append(index.Files, TarIndexEntry{
			Path:    hdr.Name,
			Size:    hdr.Size,
			Mode:    fmt.Sprintf("%04o", hdr.Mode&0o7777),
			ModTime: hdr.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
			IsDir:   hdr.Typeflag == tar.TypeDir,
		})
	}

	out, err := os.Create(archivePath + IndexSuffix) // #nosec G304 — derived from engine-controlled archivePath
	if err != nil {
		return fmt.Errorf("creating index sidecar: %w", err)
	}
	defer out.Close()

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&index); err != nil {
		return fmt.Errorf("encoding index sidecar: %w", err)
	}
	return nil
}

// ReadTarIndex parses a tar-index JSON file produced by WriteTarIndex.
// Returns an error if the file is missing or the schema version is unknown.
func ReadTarIndex(r io.Reader) (TarIndex, error) {
	var idx TarIndex
	if err := json.NewDecoder(r).Decode(&idx); err != nil {
		return idx, fmt.Errorf("decoding tar index: %w", err)
	}
	if idx.Version != tarIndexVersion {
		return idx, fmt.Errorf("unsupported tar index version %d (expected %d)", idx.Version, tarIndexVersion)
	}
	return idx, nil
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

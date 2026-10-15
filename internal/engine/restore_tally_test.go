//go:build unix

package engine

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreTallyNilSafeAndContext(t *testing.T) {
	var nilTally *RestoreTally
	nilTally.Add(5)
	if nilTally.Files() != 0 || nilTally.Bytes() != 0 {
		t.Fatal("nil tally reported counts")
	}
	if RestoreTallyFrom(context.Background()) != nil {
		t.Fatal("tally found on empty context")
	}
	tally := &RestoreTally{}
	if RestoreTallyFrom(WithRestoreTally(t.Context(), tally)) != tally {
		t.Fatal("tally did not round-trip through context")
	}
}

// A selective chunked restore counts only the files it wrote (#455).
func TestFolderChunkedSelectiveRestoreTally(t *testing.T) {
	h, item, repo, id, _ := backupDirMetaTree(t)
	item.Settings = map[string]any{"path": item.Settings["path"], "restore_file_paths": []string{"3DS/b.txt"}}
	tally := &RestoreTally{}
	if err := h.RestoreChunked(WithRestoreTally(t.Context(), tally), item, repo, id, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if tally.Files() != 1 || tally.Bytes() != int64(len("3DS/b.txt")) {
		t.Fatalf("tally = %d files / %d bytes, want 1 / %d", tally.Files(), tally.Bytes(), len("3DS/b.txt"))
	}
}

// The classic tar extractor counts only the selected regular files.
func TestUntarDirectoryFilteredTally(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "data.tar")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, name := range []string{"a.txt", "big/b.bin", "c.txt"} {
		body := []byte(name + "-payload")
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	tally := &RestoreTally{}
	if err := untarDirectoryFiltered(WithRestoreTally(t.Context(), tally), archive, t.TempDir(), []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if tally.Files() != 1 || tally.Bytes() != int64(len("a.txt-payload")) {
		t.Fatalf("selective tally = %d/%d, want 1/%d", tally.Files(), tally.Bytes(), len("a.txt-payload"))
	}

	all := &RestoreTally{}
	if err := untarDirectoryFiltered(WithRestoreTally(t.Context(), all), archive, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if all.Files() != 3 {
		t.Fatalf("full tally files = %d, want 3", all.Files())
	}
}

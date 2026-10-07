package recovery

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/storage"
)

// TestReadOnlyAdapterRefusesMutations checks writes and deletes never reach
// the wrapped storage, while reads still do.
func TestReadOnlyAdapterRefusesMutations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner, err := storage.NewAdapter("local", `{"path":`+jsonString(dir)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	a := readOnlyAdapter{inner: inner}

	if err := a.Write("keep.txt", strings.NewReader("changed")); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Write = %v, want ErrReadOnly", err)
	}
	if err := a.WriteFrom("new.txt", func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil }); !errors.Is(err, ErrReadOnly) {
		t.Errorf("WriteFrom = %v, want ErrReadOnly", err)
	}
	if err := a.Delete("keep.txt"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Delete = %v, want ErrReadOnly", err)
	}

	rc, err := a.Read("keep.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "original" {
		t.Fatalf("file content = %q, want unchanged", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("WriteFrom created a file: %v", err)
	}
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

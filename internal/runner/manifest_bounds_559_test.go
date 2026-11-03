package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tinkerologie/vault/internal/bounded"
	"github.com/tinkerologie/vault/internal/storage"
)

// An oversized manifest.json is not read, and so cannot be taken for a manifest.
func TestHasManifest_RefusesOversizedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := filepath.Join(dir, "Job", "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(run, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Truncate(bounded.ManifestBytes + 1) // sparse
	_ = f.Close()
	a := storage.NewLocalAdapter(dir)
	if hasManifest(a, "Job/run") {
		t.Fatal("oversized file treated as a manifest")
	}
}

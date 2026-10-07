package recovery

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
)

func newTestExtractor(t *testing.T) *extractor {
	t.Helper()
	return &extractor{
		ctx: context.Background(), root: t.TempDir(), names: newNamer(false, false),
		report: &ItemReport{}, written: map[string]string{}, sizes: map[string]int64{},
		fromTree: map[string]bool{}, dirTimes: map[string]dirMeta{}, checked: map[string]bool{},
	}
}

// TestSymlinkRejectsDotDotAfterName checks ".." is allowed only as a leading
// run: after a folder name it could climb out through an earlier link.
func TestSymlinkRejectsDotDotAfterName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links are never recreated on Windows")
	}
	x := newTestExtractor(t)
	for display, link := range map[string]string{
		"deep/deeper/s": "../..",
		"d/ok":          "../target.txt",
	} {
		if reason := x.symlink(display, link); reason != "" {
			t.Errorf("symlink(%q -> %q) refused: %s", display, link, reason)
		}
	}
	for display, link := range map[string]string{
		"x":     "deep/deeper/s/../../..",
		"y":     "a/../..",
		"z":     "../escape",
		"abs":   "/etc/passwd",
		"empty": "",
	} {
		if reason := x.symlink(display, link); reason == "" {
			t.Errorf("symlink(%q -> %q) was created, want refused", display, link)
		}
		if _, err := os.Lstat(filepath.Join(x.root, display)); err == nil {
			t.Errorf("%s exists on disk", display)
		}
	}
}

// TestExtractDedupSkipsUnmappableNames checks one name that cannot be mapped
// (here one that escapes the item) is reported, while the rest of the item is
// recovered. Invalid UTF-8 cannot reach this path: manifests are JSON, whose
// encoder stores such bytes as U+FFFD, so that name is recovered as such.
func TestExtractDedupSkipsUnmappableNames(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	adapter, err := storage.NewAdapter("local", `{"path":`+jsonString(t.TempDir())+`}`)
	if err != nil {
		t.Fatal(err)
	}
	destID, _ := d.CreateStorageDestination(db.StorageDestination{Name: "t", Type: "local", Config: "{}", DedupEnabled: true})
	repo, err := dedup.InitRepo(d, adapter, destID, fixtureServerKey())
	if err != nil {
		t.Fatal(err)
	}
	chunk, _ := repo.Put([]byte("good"))
	id, err := repo.PutManifest("item", dedup.Manifest{Version: 1, Files: map[string]dedup.ManifestEntry{
		"good.txt":     {Size: 4, Chunks: []dedup.ID{chunk}},
		"bad\xff.txt":  {Size: 4, Chunks: []dedup.ID{chunk}},
		"../../escape": {Size: 4, Chunks: []dedup.ID{chunk}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.Flush()

	x := newTestExtractor(t)
	if err := (&Session{}).extractDedup(x, repo, id, Item{Name: "item", Type: "folder"}); err != nil {
		t.Fatalf("extractDedup failed the whole item: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(x.root, "good.txt")); err != nil || string(got) != "good" {
		t.Fatalf("good.txt = %q, %v", got, err)
	}
	if len(x.report.Skipped) != 1 || x.report.Skipped[0].Path != "../../escape" {
		t.Fatalf("skipped = %+v, want only ../../escape", x.report.Skipped)
	}
	if _, err := os.Stat(filepath.Join(x.root, "bad\uFFFD.txt")); err != nil {
		t.Fatalf("the non-UTF-8 name should be recovered as U+FFFD: %v", err)
	}
}

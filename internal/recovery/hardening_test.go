package recovery

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/storage"
)

func newTestExtractor(t *testing.T) *extractor {
	t.Helper()
	root := t.TempDir()
	handle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return &extractor{
		ctx: context.Background(), root: root, handle: handle, names: newNamer(false, false),
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

func TestOpenItemRootRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "item")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := openItemRoot(link); err == nil {
		t.Fatal("symlinked item folder accepted")
	}
	if _, err := openItemRoot(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing folder accepted")
	}
	handle, err := openItemRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = handle.Close()
}

// Issue #545: once a folder has been checked, swapping it for a symlink must not
// lead later writes out of the item.
func TestWriteFileSurvivesSwappedDirectory(t *testing.T) {
	x := newTestExtractor(t)
	outside := t.TempDir()
	if _, err := x.writeFile("sub/first", strings.NewReader("a"), 0o640, time.Time{}); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(x.root, "sub")
	if err := os.Rename(sub, sub+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, sub); err != nil {
		t.Fatal(err)
	}
	if _, err := x.writeFile("sub/second", strings.NewReader("b"), 0o640, time.Time{}); err == nil {
		t.Fatal("wrote through a swapped symlink")
	}
	if _, err := os.Lstat(filepath.Join(outside, "second")); !os.IsNotExist(err) {
		t.Fatalf("file created outside the item: %v", err)
	}
}

func TestExtractorWritesThroughItemHandle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links are never recreated on Windows")
	}
	x := newTestExtractor(t)
	stamp := time.Unix(1000, 0)
	if err := x.mkdir("d/e", 0o750, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := x.writeFile("d/f", strings.NewReader("data"), 0o640, stamp); err != nil {
		t.Fatal(err)
	}
	if err := x.copyHardlink("d/g", "d/f", 0o640, stamp); err != nil {
		t.Fatal(err)
	}
	if reason := x.symlink("d/link", "f"); reason != "" {
		t.Fatalf("symlink: %s", reason)
	}
	// Replacing an existing link is allowed; replacing a file is not.
	if reason := x.symlink("d/link", "g"); reason != "" {
		t.Fatalf("relink: %s", reason)
	}
	if reason := x.symlink("d/f", "g"); reason == "" {
		t.Fatal("symlink replaced a regular file")
	}
	if _, err := x.writeFile("d/link", strings.NewReader("x"), 0o640, stamp); err == nil {
		t.Fatal("file written over a symlink")
	}
	if err := x.finish(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(x.root, "d", "e"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Fatalf("directory mtime = %v", info.ModTime())
	}
	// Metadata is never applied through a symlink.
	x.applyMeta(filepath.Join("d", "link"), 0o777, stamp)
	target, err := os.Stat(filepath.Join(x.root, "d", "g"))
	if err != nil {
		t.Fatal(err)
	}
	if target.Mode().Perm() == 0o777 {
		t.Fatal("followed the link")
	}
	x.applyMeta("missing", 0o777, stamp)
}

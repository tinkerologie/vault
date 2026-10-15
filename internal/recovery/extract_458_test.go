package recovery

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/dedup"
)

func TestExtract458_OptionAndPointGuards(t *testing.T) {
	s := &Session{}
	if _, err := s.Extract(context.Background(), Point{Locked: true, LockedBy: "age", StoragePath: "p"}, ExtractOptions{Dest: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("locked point: %v", err)
	}
	if _, err := s.Extract(context.Background(), Point{}, ExtractOptions{}); err == nil || !strings.Contains(err.Error(), "destination folder is required") {
		t.Errorf("no dest: %v", err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Extract(context.Background(), Point{}, ExtractOptions{Dest: filepath.Join(blocker, "sub")}); err == nil || !strings.Contains(err.Error(), "create destination") {
		t.Errorf("dest under a file: %v", err)
	}
	if _, err := s.Extract(context.Background(), Point{StoragePath: "p"}, ExtractOptions{Dest: t.TempDir(), Items: []string{"ghost"}}); err == nil || !strings.Contains(err.Error(), `named "ghost"`) {
		t.Errorf("unknown item: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := Point{Items: []Item{{Name: "a", Type: "folder"}}}
	if _, err := s.Extract(ctx, p, ExtractOptions{Dest: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	if !(Report{Items: []ItemReport{{}, {Error: "x"}}}).Failed() || (Report{Items: []ItemReport{{}}}).Failed() {
		t.Error("Report.Failed misreports")
	}
}

func TestExtract458_PrepareRoot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareRoot(file, false); err == nil || !strings.Contains(err.Error(), "not a folder") {
		t.Errorf("file root: %v", err)
	}
	if err := prepareRoot(dir, false); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("non-empty root: %v", err)
	}
	if err := prepareRoot(dir, true); err != nil {
		t.Errorf("non-empty root with overwrite: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := prepareRoot(empty, false); err != nil {
		t.Errorf("empty root: %v", err)
	}
}

func TestExtract458_TargetRefusesBadParents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures need POSIX links")
	}
	x := newTestExtractor(t)
	if err := os.WriteFile(filepath.Join(x.root, "plain"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := x.target("plain/child"); err == nil || !strings.Contains(err.Error(), "not a folder") {
		t.Errorf("file parent: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(x.root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := x.target("link/child"); err == nil || !strings.Contains(err.Error(), "through symlink") {
		t.Errorf("symlink parent: %v", err)
	}
	if reason := x.symlink("link/s", "x"); reason == "" {
		t.Error("symlink under a linked folder should be refused")
	}
}

func TestExtract458_WriteFileGuards(t *testing.T) {
	x := newTestExtractor(t)
	if err := os.Mkdir(filepath.Join(x.root, "adir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := x.writeFile("adir", strings.NewReader("x"), 0o644, time.Time{}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("dir target: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	x.ctx = ctx
	if _, err := x.writeFile("f", strings.NewReader("x"), 0o644, time.Time{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled write: %v", err)
	}
	if err := x.finish(); !errors.Is(err, context.Canceled) {
		t.Errorf("finish after cancel: %v", err)
	}
}

func TestExtract458_SymlinkExistingPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links are never recreated on Windows")
	}
	x := newTestExtractor(t)
	if _, err := x.writeFile("file", strings.NewReader("x"), 0o644, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if reason := x.symlink("file", "other"); !strings.Contains(reason, "already exists") {
		t.Errorf("over a file: %q", reason)
	}
	if reason := x.symlink("s", "file"); reason != "" {
		t.Fatalf("first link: %q", reason)
	}
	if reason := x.symlink("s", "file"); reason != "" {
		t.Errorf("replacing an existing link: %q", reason)
	}
	// A link whose existing target already resolves outside the item.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(x.root, "escape")); err != nil {
		t.Fatal(err)
	}
	if reason := x.symlink("via", "escape"); !strings.Contains(reason, "resolves outside") {
		t.Errorf("through an escaping link: %q", reason)
	}
}

func TestExtract458_CopyHardlink(t *testing.T) {
	x := newTestExtractor(t)
	if err := x.copyHardlink("b", "missing", 0o644, time.Time{}); err == nil || !strings.Contains(err.Error(), "was not extracted") {
		t.Errorf("unextracted target: %v", err)
	}
	if _, err := x.writeFile("a", strings.NewReader("data"), 0o644, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(x.written["a"]); err != nil {
		t.Fatal(err)
	}
	if err := x.copyHardlink("b", "a", 0o644, time.Time{}); err == nil {
		t.Error("vanished source: want error")
	}
}

func TestExtract458_UntarEntryKinds(t *testing.T) {
	x := newTestExtractor(t)
	archive := buildTar(t, []tarEntry{
		{name: "dir/", typ: tar.TypeDir},
		{name: "dir/a.txt", body: "alpha", typ: tar.TypeReg},
		{name: "dir/hard", link: "dir/a.txt", typ: tar.TypeLink},
		{name: "dir/hard-missing", link: "dir/nope", typ: tar.TypeLink},
		{name: "dir/hard-out", link: "../../etc/passwd", typ: tar.TypeLink},
		{name: "dir/fifo", typ: tar.TypeFifo},
		{name: "../escape", body: "x", typ: tar.TypeReg},
		{name: "skipme/b.txt", body: "beta", typ: tar.TypeReg},
	})
	x.include = []string{"dir"}
	if err := x.untar(strings.NewReader(archive), treeArchive{}); err != nil {
		t.Fatalf("untar: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(x.root, "dir", "hard")); err != nil || string(b) != "alpha" {
		t.Errorf("hard link copy = %q, %v", b, err)
	}
	skipped := skippedPaths(*x.report)
	for _, p := range []string{"dir/hard-missing", "dir/hard-out", "dir/fifo", "../escape"} {
		if _, ok := skipped[p]; !ok {
			t.Errorf("%s not reported as skipped (got %v)", p, skipped)
		}
	}
	if _, err := os.Stat(filepath.Join(x.root, "skipme")); !os.IsNotExist(err) {
		t.Errorf("excluded path extracted: %v", err)
	}

	// Single-file tree: the one entry lands at the prefix itself.
	y := newTestExtractor(t)
	if err := y.untar(strings.NewReader(buildTar(t, []tarEntry{{name: "whatever", body: "solo", typ: tar.TypeReg}})), treeArchive{prefix: "conf.yml", isFile: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(y.root, "conf.yml")); string(b) != "solo" {
		t.Errorf("single-file tree = %q", b)
	}

	// A corrupt archive surfaces a read error.
	z := newTestExtractor(t)
	if err := z.untar(strings.NewReader(strings.Repeat("not a tar header ", 64)), treeArchive{}); err == nil || !strings.Contains(err.Error(), "read archive") {
		t.Errorf("corrupt archive: %v", err)
	}
}

func TestExtract458_DedupFailures(t *testing.T) {
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	s := &Session{}
	x := newTestExtractor(t)

	var unknown dedup.ID
	unknown[0] = 1
	if err := s.extractDedup(x, repo, unknown, Item{Name: "i", Type: "folder"}); err == nil || !strings.Contains(err.Error(), "read dedup manifest") {
		t.Errorf("unknown manifest: %v", err)
	}

	cid, err := repo.Put([]byte("four"))
	if err != nil {
		t.Fatal(err)
	}
	mid, err := repo.PutManifest("i", dedup.Manifest{Version: dedup.ManifestVersion, Item: "i", Files: map[string]dedup.ManifestEntry{
		"short.txt":   {Size: 99, Chunks: []dedup.ID{cid}},
		"missing.txt": {Size: 4, Chunks: []dedup.ID{unknown}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	// Sorted walk: missing.txt first, whose chunk cannot be read.
	if err := s.extractDedup(x, repo, mid, Item{Name: "i", Type: "folder"}); err == nil || !strings.Contains(err.Error(), "read chunk") {
		t.Errorf("missing chunk: %v", err)
	}
	x.include = []string{"short.txt"}
	if err := s.extractDedup(x, repo, mid, Item{Name: "i", Type: "folder"}); err == nil || !strings.Contains(err.Error(), "backup recorded 99") {
		t.Errorf("size mismatch: %v", err)
	}
}

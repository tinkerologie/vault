package tempdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateRestoreDirOutsideAvoidsTarget(t *testing.T) {
	if _, _, err := CreateRestoreDirOutside("", "/"); err == nil || !strings.Contains(err.Error(), "outside the reviewed target") {
		t.Fatalf("staging under the root target: %v", err)
	}
	if allowedStagingBase(filepath.Join(t.TempDir(), "missing"), StageDirName, []string{"/elsewhere"}) {
		t.Fatal("unresolvable staging base was allowed")
	}

	target := t.TempDir()
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	dir, cleanup, err := CreateRestoreDirOutside(target, resolvedTarget)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(resolvedTarget, resolvedDir); err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("staging %s sits inside the reviewed target %s", resolvedDir, resolvedTarget)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("override inside the target was used: %v %v", entries, err)
	}
}

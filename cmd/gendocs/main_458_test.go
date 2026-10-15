package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerate_WritesAllPages(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "docs", "reference")
	var buf bytes.Buffer
	if err := generate(outDir, &buf); err != nil {
		t.Fatalf("generate: %v", err)
	}
	want := []string{
		"app-settings.md", "notifications.md", "job-config.md",
		"storage-sftp.md", "storage-smb.md", "storage-nfs.md",
		"storage-webdav.md", "storage-s3.md", "storage-local.md",
	}
	for _, name := range want {
		info, err := os.Stat(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", name)
		}
	}
	if got := buf.String(); !strings.Contains(got, "wrote 9 pages to "+outDir) {
		t.Errorf("unexpected output %q", got)
	}
}

func TestGenerate_OutDirIsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := generate(file, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "create ") {
		t.Fatalf("expected create error, got %v", err)
	}
}

func TestGenerate_ReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	err := generate(dir, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "write ") {
		t.Fatalf("expected write error, got %v", err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

func TestGenerate_OutputWriteError(t *testing.T) {
	if err := generate(t.TempDir(), failWriter{}); err == nil {
		t.Fatal("expected writer error")
	}
}

func TestRepoRoot(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repoRoot %q has no go.mod: %v", root, err)
	}
}

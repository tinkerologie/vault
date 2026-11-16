package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFolderDrillChecksTreeAndContent(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	manifest := filepath.Join(parent, "manifest.json")
	var output bytes.Buffer
	if err := run(t.Context(), []string{"folder-create", source, manifest}, &output); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if !strings.Contains(string(contents), emptySHA256) {
		t.Fatal("fixture manifest does not contain the digest of the empty file")
	}
	if err := run(t.Context(), []string{"folder-check", source, manifest}, &output); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(source, "nested", "empty")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "unexpected"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "note.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"folder-check", source, manifest}, &output)
	for _, message := range []string{"missing nested/empty", "unexpected unexpected", "changed note.txt"} {
		if err == nil || !strings.Contains(err.Error(), message) {
			t.Errorf("expected %q in error, got %v", message, err)
		}
	}
}

func TestFolderDrillRefusesSymlinkAndInTreeManifest(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	manifest := filepath.Join(parent, "manifest.json")
	var output bytes.Buffer
	if err := run(t.Context(), []string{"folder-create", source, manifest}, &output); err != nil {
		t.Fatal(err)
	}
	err := run(t.Context(), []string{"folder-check", source, filepath.Join(source, "manifest.json")}, &output)
	if err == nil || !strings.Contains(err.Error(), "drill manifest must be outside the fixture directory") {
		t.Fatalf("expected a refusal for an in-tree manifest, got %v", err)
	}
	if err := os.Symlink(manifest, filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"folder-check", source, manifest}, &output); err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("expected a refusal for a symlink, got %v", err)
	}
}

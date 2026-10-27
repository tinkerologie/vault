package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunReleasePipeline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	changelog := writeFixture(t, dir, "CHANGELOG.md", "## [v2026.10.03]\n\n### Fixed\n\n- current\n## [v2026.10.02]\n- old")
	manifest := writeFixture(t, dir, "vault.plg", fixtureManifest)
	notes := filepath.Join(dir, "notes.md")
	if err := run([]string{"extract", "--version", "2026.10.03", "--changelog", changelog, "--output", notes}); err != nil {
		t.Fatal(err)
	}
	args := []string{"update-plg", "--version", "2026.10.03", "--manifest", manifest, "--notes", notes}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode().Perm() != 0o640 {
		t.Fatal("manifest permissions changed")
	}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(manifest)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("idempotent update rewrote file: %v", err)
	}
	got, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	info, err := inspectManifest(string(got))
	if err != nil || info.notes != "\n## v2026.10.03\n\n### Fixed\n\n- current\n" {
		t.Fatalf("manifest notes = %q, %v", info.notes, err)
	}
}

func TestRunFailuresPreserveManifest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manifest := writeFixture(t, dir, "vault.plg", fixtureManifest)
	notes := writeFixture(t, dir, "notes.md", "- current")
	empty := writeFixture(t, dir, "empty.md", "")
	for _, args := range [][]string{
		nil, {"unknown"}, {"extract", "--unknown"}, {"extract"}, {"update-plg"},
		{"extract", "--version", "2026.10.03", "--changelog", "missing", "--output", notes},
		{"extract", "--version", "2026.10.03", "--changelog", empty, "--output", notes},
		{"extract", "--version", "2026.10.03", "--changelog", notes, "--output", notes, "extra"},
		{"update-plg", "--version", "2026.10.03", "--manifest", manifest, "--notes", "missing"},
		{"update-plg", "--version", "2026.10.03", "--manifest", manifest, "--notes", empty},
		{"update-plg", "--version", "2026.10.04", "--manifest", manifest, "--notes", notes},
	} {
		if err := run(args); err == nil {
			t.Fatalf("run(%v) should fail", args)
		}
		got, err := os.ReadFile(manifest)
		if err != nil || string(got) != fixtureManifest {
			t.Fatalf("failed update changed original: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("temporary files leaked: %d entries, %v", len(entries), err)
	}
}

func TestAtomicWriteFailures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := atomicWrite(filepath.Join(dir, "missing", "notes.md"), "- notes", 0o644); err == nil {
		t.Fatal("expected missing directory failure")
	}
	if err := atomicWrite(dir, "- notes", 0o644); err == nil {
		t.Fatal("expected rename onto directory failure")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary files leaked: %v", err)
	}
	changelog := writeFixture(t, dir, "CHANGELOG.md", "## [v2026.10.03]\n- current")
	if err := run([]string{"extract", "--version", "2026.10.03", "--changelog", changelog, "--output", dir}); err == nil {
		t.Fatal("expected output write failure")
	}
}

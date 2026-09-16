package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// symlinkedVolume creates a real directory and a symlink pointing at it,
// skipping the test on a filesystem that cannot make symlinks.
func symlinkedVolume(t *testing.T) (link, real string) {
	t.Helper()
	base := t.TempDir()
	real = filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}
	return link, real
}

// TestContainerBackupSymlinkedSourceDetectsStaleMtimeNewFile is the regression
// test for issue #353. A bind mount whose source is a symlink used to bypass
// the issue #320 listing-aware detection entirely — os.Lstat classified the
// symlinked root as a non-directory, so change detection returned through the
// mtime-only branch before the parent listing was ever consulted, and the
// volume silently degraded to mtime-only filtering.
//
// A NEW file with a stale mtime (the cp -a case) must reach the differential
// archive through a symlinked source exactly as it does through a plain one.
func TestContainerBackupSymlinkedSourceDetectsStaleMtimeNewFile(t *testing.T) {
	t.Parallel()

	changedSince := time.Now().Add(-1 * time.Hour)
	stale := time.Now().Add(-3 * time.Hour)

	link, real := symlinkedVolume(t)
	writeStale := func(rel string) {
		t.Helper()
		p := filepath.Join(real, rel)
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	writeStale("old.txt")

	created := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)

	// Full backup through the symlink.
	fullDest := t.TempDir()
	fullItem := BackupItem{
		Name: "test", Type: "container",
		Settings:    map[string]any{"id": "abc123"},
		Compression: CompressionNone,
	}
	if _, err := (&ContainerHandler{cli: newClassicMock(t, false, link, created)}).
		Backup(context.Background(), fullItem, fullDest, noopProgress); err != nil {
		t.Fatalf("full Backup: %v", err)
	}

	volBase := volumeArchiveBase(link)
	if names := listTarEntries(t, filepath.Join(fullDest, volBase)); !containsName(names, "old.txt") {
		t.Fatalf("full archive missing old.txt — the symlinked source was not followed: %v", names)
	}

	// The manifest must record what the source resolved to, so the next
	// differential can prove the mount still points at the same tree.
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readManifestForTest(t, fullDest)
	if len(manifest) != 1 {
		t.Fatalf("got %d manifest entries, want 1", len(manifest))
	}
	if manifest[0].Source != link {
		t.Errorf("manifest source = %q, want the raw mount source %q", manifest[0].Source, link)
	}
	if manifest[0].ResolvedSource != resolvedReal {
		t.Errorf("manifest resolved_source = %q, want %q", manifest[0].ResolvedSource, resolvedReal)
	}

	// A NEW file whose mtime predates changed_since — invisible to mtime-only
	// filtering, and the whole point of the parent listing.
	writeStale("added.txt")

	diffDest := t.TempDir()
	diffItem := BackupItem{
		Name: "test", Type: "container",
		Settings: map[string]any{
			"id":                        "abc123",
			"changed_since":             changedSince.UTC().Format(time.RFC3339),
			"prev_volume_listing_paths": map[string][]string{link: {"old.txt"}},
			"prev_volume_resolved_sources": map[string]string{
				link: resolvedReal,
			},
		},
		Compression: CompressionNone,
	}
	if _, err := (&ContainerHandler{cli: newClassicMock(t, false, link, created)}).
		Backup(context.Background(), diffItem, diffDest, noopProgress); err != nil {
		t.Fatalf("differential Backup: %v", err)
	}

	archive := filepath.Join(diffDest, volBase)
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("differential volume was skipped (archive missing): %v", err)
	}
	names := listTarEntries(t, archive)
	if !containsName(names, "added.txt") {
		t.Errorf("differential archive missing added.txt — the symlinked source fell back to mtime-only filtering: %v", names)
	}
	if containsName(names, "old.txt") {
		t.Errorf("differential archive should skip unchanged old.txt: %v", names)
	}
}

// TestContainerBackupRepointedSymlinkCapturesInFull pins the identity guard:
// when the parent recorded a different resolution for the same mount, its
// listing describes a DIFFERENT tree. A file in the new tree whose relative
// path happens to match one in that listing would read as "not new" and be
// filtered out on mtime — the issue #320 data-loss class again. The volume is
// captured in full instead.
func TestContainerBackupRepointedSymlinkCapturesInFull(t *testing.T) {
	t.Parallel()

	changedSince := time.Now().Add(-1 * time.Hour)
	stale := time.Now().Add(-3 * time.Hour)

	link, real := symlinkedVolume(t)
	p := filepath.Join(real, "old.txt")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, stale, stale); err != nil {
		t.Fatal(err)
	}

	created := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	dest := t.TempDir()
	item := BackupItem{
		Name: "test", Type: "container",
		Settings: map[string]any{
			"id":                        "abc123",
			"changed_since":             changedSince.UTC().Format(time.RFC3339),
			"prev_volume_listing_paths": map[string][]string{link: {"old.txt"}},
			// The parent resolved this mount somewhere else entirely.
			"prev_volume_resolved_sources": map[string]string{link: "/mnt/user/somewhere-else"},
		},
		Compression: CompressionNone,
	}
	if _, err := (&ContainerHandler{cli: newClassicMock(t, false, link, created)}).
		Backup(context.Background(), item, dest, noopProgress); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	archive := filepath.Join(dest, volumeArchiveBase(link))
	names := listTarEntries(t, archive)
	if !containsName(names, "old.txt") {
		t.Errorf("a repointed mount must be captured in full, stale mtimes included: %v", names)
	}
	if _, err := os.Stat(archive + ListingSuffix); err != nil {
		t.Errorf("a full capture must still write a fresh listing baseline: %v", err)
	}
}

// readManifestForTest decodes a staged backup's volumes.json.
func readManifestForTest(t *testing.T, dir string) []volumeManifestEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "volumes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest []volumeManifestEntry
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// TestReuseParentVolumeListing pins when a parent listing may be trusted.
// A parent that recorded no resolution predates issue #353; forcing a full
// capture for it would re-archive every volume of every container on the
// first differential after an upgrade, so the pre-existing behaviour is kept.
func TestReuseParentVolumeListing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		source       string
		resolved     string
		prevResolved map[string]string
		want         bool
	}{
		{name: "no parent record at all", source: "/a", resolved: "/real/a", want: true},
		{name: "parent recorded an empty resolution", source: "/a", resolved: "/real/a", prevResolved: map[string]string{"/a": ""}, want: true},
		{name: "parent recorded the same resolution", source: "/a", resolved: "/real/a", prevResolved: map[string]string{"/a": "/real/a"}, want: true},
		{name: "parent recorded a different resolution", source: "/a", resolved: "/real/a", prevResolved: map[string]string{"/a": "/other/a"}, want: false},
		{name: "another volume's record does not apply", source: "/a", resolved: "/real/a", prevResolved: map[string]string{"/b": "/other/b"}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := reuseParentVolumeListing(tc.source, tc.resolved, tc.prevResolved); got != tc.want {
				t.Errorf("reuseParentVolumeListing() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveMountSource pins the best-effort contract: an unresolvable path
// comes back unchanged so the caller's stat reports the real error.
func TestResolveMountSource(t *testing.T) {
	t.Parallel()

	link, real := symlinkedVolume(t)
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveMountSource(link); got != resolvedReal {
		t.Errorf("resolveMountSource(symlink) = %q, want %q", got, resolvedReal)
	}
	missing := filepath.Join(t.TempDir(), "gone")
	if got := resolveMountSource(missing); got != missing {
		t.Errorf("resolveMountSource(missing) = %q, want it returned unchanged", got)
	}
}

// TestPrevVolumeResolvedSources pins both wire forms: the typed map from a
// direct runner->engine call and the JSON-decoded map from a settings blob.
func TestPrevVolumeResolvedSources(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		settings map[string]any
		want     map[string]string
	}{
		{name: "absent", settings: map[string]any{}},
		{name: "nil value", settings: map[string]any{"prev_volume_resolved_sources": nil}},
		{
			name:     "typed map",
			settings: map[string]any{"prev_volume_resolved_sources": map[string]string{"/a": "/real/a"}},
			want:     map[string]string{"/a": "/real/a"},
		},
		{
			name:     "json-decoded map skips non-string values",
			settings: map[string]any{"prev_volume_resolved_sources": map[string]any{"/a": "/real/a", "/b": 7}},
			want:     map[string]string{"/a": "/real/a"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := prevVolumeResolvedSources(tc.settings)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("prevVolumeResolvedSources() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("prevVolumeResolvedSources() = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

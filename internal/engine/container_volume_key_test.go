package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"
	imagetypes "github.com/moby/moby/api/types/image"
	mounttypes "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// newTwoMountMock builds a classic-path mock whose container reports the two
// given bind mounts in exactly the order supplied, so a test can replay the
// same container with its mounts reordered.
func newTwoMountMock(t *testing.T, mounts ...containertypes.MountPoint) *mockDockerClient {
	t.Helper()
	return &mockDockerClient{
		inspectResp: client.ContainerInspectResult{
			Container: containertypes.InspectResponse{
				ID:     "abc123",
				Name:   "/test",
				Config: &containertypes.Config{Image: "nginx:latest"},
				State:  &containertypes.State{Running: false},
				Mounts: mounts,
			},
		},
		imageResp: client.ImageInspectResult{
			InspectResponse: imagetypes.InspectResponse{
				RepoDigests: []string{"nginx@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			},
		},
	}
}

// TestBackupNamesVolumeArchivesByStableKey is the regression test for issue
// #352. A container recreate can make Docker report the same mounts in a
// different order. Under the old index-based naming the SAME volume was
// written to volume_0.tar in one run and volume_1.tar in the next, so a
// differential's archive was later merged against a different volume's base
// full. Naming by mount source makes each volume's archive name identical
// across both runs.
func TestBackupNamesVolumeArchivesByStableKey(t *testing.T) {
	t.Parallel()

	confSrc := t.TempDir()
	dataSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(confSrc, "conf.txt"), []byte("conf"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataSrc, "data.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	confMount := containertypes.MountPoint{Type: mounttypes.TypeBind, Source: confSrc, Destination: "/config"}
	dataMount := containertypes.MountPoint{Type: mounttypes.TypeBind, Source: dataSrc, Destination: "/data"}

	item := BackupItem{
		Name:        "test",
		Type:        "container",
		Settings:    map[string]any{"id": "abc123"},
		Compression: CompressionNone,
	}

	// Run 1: mounts reported as (config, data).
	firstDest := t.TempDir()
	if _, err := (&ContainerHandler{cli: newTwoMountMock(t, confMount, dataMount)}).
		Backup(context.Background(), item, firstDest, noopProgress); err != nil {
		t.Fatalf("first Backup: %v", err)
	}

	// Run 2: the container was recreated and Docker now reports (data, config).
	secondDest := t.TempDir()
	if _, err := (&ContainerHandler{cli: newTwoMountMock(t, dataMount, confMount)}).
		Backup(context.Background(), item, secondDest, noopProgress); err != nil {
		t.Fatalf("second Backup: %v", err)
	}

	for _, tc := range []struct {
		name    string
		src     string
		wantFil string
	}{
		{name: "config volume", src: confSrc, wantFil: "conf.txt"},
		{name: "data volume", src: dataSrc, wantFil: "data.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := volumeArchiveBase(tc.src)
			for _, dir := range []string{firstDest, secondDest} {
				archive := filepath.Join(dir, base)
				if _, err := os.Stat(archive); err != nil {
					t.Fatalf("archive %s missing in %s: %v", base, dir, err)
				}
				// The archive named for this source must hold this source's
				// file — the union of both runs must never cross volumes.
				if names := listTarEntries(t, archive); !containsName(names, tc.wantFil) {
					t.Errorf("archive %s holds %v, want %s", base, names, tc.wantFil)
				}
			}
		})
	}

	// The legacy index names must no longer be produced at all, or a chain
	// merge across a mixed-version chain would silently union two volumes.
	for _, dir := range []string{firstDest, secondDest} {
		for i := range 2 {
			if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("volume_%d.tar", i))); err == nil {
				t.Errorf("index-named archive volume_%d.tar still written in %s", i, dir)
			}
		}
	}
}

// TestVolumeArchiveBaseIsDeterministicAndDistinct pins the naming contract the
// merge and restore paths depend on: the same source always yields the same
// name, different sources never collide, and the affixes MergeContainerChain-
// Staging and findArchive rely on are preserved.
func TestVolumeArchiveBaseIsDeterministicAndDistinct(t *testing.T) {
	t.Parallel()

	a := volumeArchiveBase("/mnt/user/appdata/plex")
	b := volumeArchiveBase("/mnt/user/appdata/plex")
	c := volumeArchiveBase("/mnt/user/appdata/sonarr")

	if a != b {
		t.Errorf("volumeArchiveBase not deterministic: %q != %q", a, b)
	}
	if a == c {
		t.Errorf("distinct sources collided on %q", a)
	}
	for _, got := range []string{a, c} {
		if len(got) < len("volume_.tar") || got[:len("volume_")] != "volume_" {
			t.Errorf("%q lost the volume_ prefix MergeContainerChainStaging scans for", got)
		}
		if filepath.Ext(got) != ".tar" {
			t.Errorf("%q lost the .tar base findArchive appends suffixes to", got)
		}
	}
}

// TestResolveVolumeArchive covers the three-step lookup that keeps restore
// points written before #352 restoring: the manifest's recorded name first,
// then the stable-key name, then the legacy index name. A negative legacyIndex
// disables the legacy step, which is what the caller passes when the manifest
// exists but does not list this mount.
func TestResolveVolumeArchive(t *testing.T) {
	t.Parallel()

	const source = "/mnt/user/appdata/plex"

	cases := []struct {
		name            string
		present         []string // files created in sourceDir
		manifestArchive string
		legacyIndex     int
		want            string
		wantErr         bool
	}{
		{
			name:            "manifest name wins",
			present:         []string{volumeArchiveBase(source), "volume_3.tar"},
			manifestArchive: volumeArchiveBase(source),
			legacyIndex:     3,
			want:            volumeArchiveBase(source),
		},
		{
			name:            "manifest compression suffix falls back to the merged plain base",
			present:         []string{volumeArchiveBase(source)},
			manifestArchive: volumeArchiveBase(source) + ".zst",
			legacyIndex:     0,
			want:            volumeArchiveBase(source),
		},
		{
			name:        "no manifest entry resolves by stable key",
			present:     []string{volumeArchiveBase(source)},
			legacyIndex: 0,
			want:        volumeArchiveBase(source),
		},
		{
			name:        "legacy index-named archive still resolves",
			present:     []string{"volume_2.tar"},
			legacyIndex: 2,
			want:        "volume_2.tar",
		},
		{
			name:        "legacy gzip suffix still resolves",
			present:     []string{"volume_1.tar.gz"},
			legacyIndex: 1,
			want:        "volume_1.tar.gz",
		},
		{
			// The backup-time index is authoritative; the caller must never
			// pass the current loop position, which a recreate can shift.
			name:        "the recorded index is the one probed",
			present:     []string{"volume_0.tar", "volume_5.tar"},
			legacyIndex: 5,
			want:        "volume_5.tar",
		},
		{
			// A manifest that does not list this mount disables the fallback:
			// resolving an index there would hand this mount a DIFFERENT
			// volume's archive, which is exactly the #352 mis-pairing.
			name:        "disabled index fallback does not resolve a legacy archive",
			present:     []string{"volume_0.tar", "volume_1.tar"},
			legacyIndex: -1,
			wantErr:     true,
		},
		{
			name:        "nothing present is an error",
			present:     nil,
			legacyIndex: 0,
			wantErr:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tc.present {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := resolveVolumeArchive(dir, tc.manifestArchive, source, tc.legacyIndex)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveVolumeArchive() error = %v", err)
			}
			if got != filepath.Join(dir, tc.want) {
				t.Errorf("resolveVolumeArchive() = %q, want %q", got, filepath.Join(dir, tc.want))
			}
		})
	}
}

// TestFindVolumeManifestEntryCorrelatesBySource proves the manifest lookup is
// order-independent, and that the index fallback applies only to entries that
// recorded no source at all — returning another volume's entry would hand the
// restore the wrong IsFile / root-metadata.
func TestFindVolumeManifestEntryCorrelatesBySource(t *testing.T) {
	t.Parallel()

	manifest := []volumeManifestEntry{
		{Index: 0, Source: "/srv/config", Destination: "/config", BackedUp: true, RootMode: 0o750},
		{Index: 1, Source: "/srv/data", Destination: "/data", BackedUp: true, IsFile: true},
	}

	cases := []struct {
		name      string
		manifest  []volumeManifestEntry
		source    string
		index     int
		wantFound bool
		wantDest  string
	}{
		{
			name:      "matches by source when the index shifted",
			manifest:  manifest,
			source:    "/srv/data",
			index:     0, // reordered: data is now first
			wantFound: true,
			wantDest:  "/data",
		},
		{
			name:      "matches by source at its original index",
			manifest:  manifest,
			source:    "/srv/config",
			index:     0,
			wantFound: true,
			wantDest:  "/config",
		},
		{
			name:      "unknown source does not fall back to another volume",
			manifest:  manifest,
			source:    "/srv/added-later",
			index:     0,
			wantFound: false,
		},
		{
			name:      "source-less legacy entry falls back to the index",
			manifest:  []volumeManifestEntry{{Index: 1, Destination: "/legacy", BackedUp: true}},
			source:    "/srv/whatever",
			index:     1,
			wantFound: true,
			wantDest:  "/legacy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := findVolumeManifestEntry(tc.manifest, tc.source, tc.index)
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if found && got.Destination != tc.wantDest {
				t.Errorf("Destination = %q, want %q", got.Destination, tc.wantDest)
			}
		})
	}
}

// TestMergeContainerChainKeepsReorderedVolumesSeparate is the chain-level
// regression for #352. Two restore points back up the same two volumes, with
// the mounts reported in opposite order. Merging the chain must overlay each
// volume onto itself; under index naming the two volumes' contents were
// unioned into each other's archive.
func TestMergeContainerChainKeepsReorderedVolumesSeparate(t *testing.T) {
	t.Parallel()

	const confSrc = "/srv/config"
	const dataSrc = "/srv/data"

	// Build one step dir holding both volumes, each named by its stable key.
	newStep := func(t *testing.T, confFile, dataFile string) string {
		t.Helper()
		step := t.TempDir()
		for _, v := range []struct{ src, file string }{{confSrc, confFile}, {dataSrc, dataFile}} {
			tree := filepath.Join(step, "tree-"+filepath.Base(v.src))
			if err := os.MkdirAll(tree, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tree, v.file), []byte(v.file), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := tarDirectory(context.Background(), tree, filepath.Join(step, volumeArchiveBase(v.src)), nil, CompressionNone); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(tree); err != nil {
				t.Fatal(err)
			}
		}
		return step
	}

	// Full step, then a differential taken after a recreate reordered the
	// mounts. The archive names are unaffected by that reorder.
	full := newStep(t, "conf-base.txt", "data-base.txt")
	diff := newStep(t, "conf-new.txt", "data-new.txt")

	outDir := t.TempDir()
	if err := MergeContainerChainStaging(context.Background(), []string{full, diff}, outDir); err != nil {
		t.Fatalf("MergeContainerChainStaging() error = %v", err)
	}

	for _, tc := range []struct {
		name    string
		src     string
		want    []string
		notWant []string
	}{
		{name: "config volume", src: confSrc, want: []string{"conf-base.txt", "conf-new.txt"}, notWant: []string{"data-base.txt", "data-new.txt"}},
		{name: "data volume", src: dataSrc, want: []string{"data-base.txt", "data-new.txt"}, notWant: []string{"conf-base.txt", "conf-new.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names := listTarEntries(t, filepath.Join(outDir, volumeArchiveBase(tc.src)))
			for _, w := range tc.want {
				if !containsName(names, w) {
					t.Errorf("merged archive missing %s: %v", w, names)
				}
			}
			for _, nw := range tc.notWant {
				if containsName(names, nw) {
					t.Errorf("merged archive leaked %s from the other volume: %v", nw, names)
				}
			}
		})
	}
}

// writeStepVolume tars a one-file tree into stepDir under the given archive
// base name, the way one chain step's backup would have written it.
func writeStepVolume(t *testing.T, stepDir, archiveBase, fileName string) {
	t.Helper()
	tree := filepath.Join(stepDir, "tree-"+archiveBase)
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, fileName), []byte(fileName), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tarDirectory(context.Background(), tree, filepath.Join(stepDir, archiveBase), nil, CompressionNone); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(tree); err != nil {
		t.Fatal(err)
	}
}

// writeStepManifest writes a step's volumes.json for the given source ->
// archive-name pairs.
func writeStepManifest(t *testing.T, stepDir string, archiveBySource map[string]string) {
	t.Helper()
	manifest := make([]volumeManifestEntry, 0, len(archiveBySource))
	i := 0
	for source, archive := range archiveBySource {
		manifest = append(manifest, volumeManifestEntry{
			Index: i, Source: source, Destination: "/mnt" + source, BackedUp: true, Archive: archive,
		})
		i++
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stepDir, "volumes.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestMergeContainerChainSpanningTheNamingChange is the regression test for the
// mixed-version chain. A base full written BEFORE #352 is index-named; a later
// differential written after it is stable-key named. Grouping the merge by raw
// filename would leave the two as separate archives, so restore would take the
// differential alone and drop every unchanged file the base full held — the
// issue #320 data-loss class, reintroduced across an upgrade. Canonicalising
// each step's archive names through its volumes.json must overlay them.
func TestMergeContainerChainSpanningTheNamingChange(t *testing.T) {
	t.Parallel()

	const confSrc = "/srv/config"
	const dataSrc = "/srv/data"

	cases := []struct {
		name string
		// setup returns the chain's step dirs, oldest first.
		setup func(t *testing.T) []string
	}{
		{
			name: "index-named full then stable-key differential",
			setup: func(t *testing.T) []string {
				full := t.TempDir()
				writeStepVolume(t, full, "volume_0.tar", "conf-base.txt")
				writeStepVolume(t, full, "volume_1.tar", "data-base.txt")
				writeStepManifest(t, full, map[string]string{
					confSrc: "volume_0.tar",
					dataSrc: "volume_1.tar",
				})

				diff := t.TempDir()
				writeStepVolume(t, diff, volumeArchiveBase(confSrc), "conf-new.txt")
				writeStepVolume(t, diff, volumeArchiveBase(dataSrc), "data-new.txt")
				writeStepManifest(t, diff, map[string]string{
					confSrc: volumeArchiveBase(confSrc),
					dataSrc: volumeArchiveBase(dataSrc),
				})
				return []string{full, diff}
			},
		},
		{
			// The same chain with the mounts reordered in the differential.
			// Under index naming the differential's volume_0 was the DATA
			// volume, so a filename merge would union the two volumes.
			name: "index-named full then reordered stable-key differential",
			setup: func(t *testing.T) []string {
				full := t.TempDir()
				writeStepVolume(t, full, "volume_0.tar", "conf-base.txt")
				writeStepVolume(t, full, "volume_1.tar", "data-base.txt")
				writeStepManifest(t, full, map[string]string{
					confSrc: "volume_0.tar",
					dataSrc: "volume_1.tar",
				})

				// Recreate reordered the mounts; stable-key names are
				// unaffected, which is the whole point.
				diff := t.TempDir()
				writeStepVolume(t, diff, volumeArchiveBase(dataSrc), "data-new.txt")
				writeStepVolume(t, diff, volumeArchiveBase(confSrc), "conf-new.txt")
				writeStepManifest(t, diff, map[string]string{
					dataSrc: volumeArchiveBase(dataSrc),
					confSrc: volumeArchiveBase(confSrc),
				})
				return []string{full, diff}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stepDirs := tc.setup(t)
			outDir := t.TempDir()
			if err := MergeContainerChainStaging(context.Background(), stepDirs, outDir); err != nil {
				t.Fatalf("MergeContainerChainStaging() error = %v", err)
			}

			for _, v := range []struct {
				name    string
				src     string
				want    []string
				notWant []string
			}{
				{name: "config volume", src: confSrc, want: []string{"conf-base.txt", "conf-new.txt"}, notWant: []string{"data-base.txt", "data-new.txt"}},
				{name: "data volume", src: dataSrc, want: []string{"data-base.txt", "data-new.txt"}, notWant: []string{"conf-base.txt", "conf-new.txt"}},
			} {
				t.Run(v.name, func(t *testing.T) {
					// Restore resolves via the newest manifest, which is
					// stable-key named, so that is where the merged data
					// must land.
					archive := filepath.Join(outDir, volumeArchiveBase(v.src))
					if _, err := os.Stat(archive); err != nil {
						t.Fatalf("merged archive for %s missing: %v", v.src, err)
					}
					names := listTarEntries(t, archive)
					for _, w := range v.want {
						if !containsName(names, w) {
							t.Errorf("merged archive missing %s (base-full data dropped across the naming change): %v", w, names)
						}
					}
					for _, nw := range v.notWant {
						if containsName(names, nw) {
							t.Errorf("merged archive leaked %s from the other volume: %v", nw, names)
						}
					}
				})
			}

			// The index-named archives must not survive as separate merged
			// outputs, or a restore against a legacy manifest would find a
			// base-only archive.
			for i := range 2 {
				if _, err := os.Stat(filepath.Join(outDir, fmt.Sprintf("volume_%d.tar", i))); err == nil {
					t.Errorf("merge left an un-canonicalised volume_%d.tar behind", i)
				}
			}
		})
	}
}

// TestSafeVolumeArchiveName pins the restore-boundary guard on names read out
// of a backup's volumes.json. The manifest travels with the backup and can
// come from remote storage, so a crafted entry must not be able to steer the
// restore at a file outside the staging directory.
func TestSafeVolumeArchiveName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "engine-written stable-key name", in: "volume_0a1b2c3d4e5f6071.tar", want: true},
		{name: "legacy index name", in: "volume_0.tar", want: true},
		{name: "gzip suffix", in: "volume_0.tar.gz", want: true},
		{name: "zstd suffix", in: "volume_0.tar.zst", want: true},
		{name: "empty", in: "", want: false},
		{name: "parent traversal", in: "../volume_0.tar", want: false},
		{name: "deep traversal", in: "../../etc/shadow.tar", want: false},
		{name: "absolute path", in: "/etc/volume_0.tar", want: false},
		{name: "nested path", in: "sub/volume_0.tar", want: false},
		{name: "dot", in: ".", want: false},
		{name: "dotdot", in: "..", want: false},
		{name: "wrong prefix", in: "image.tar", want: false},
		{name: "prefix but not a tar", in: "volume_0.json", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeVolumeArchiveName(tc.in); got != tc.want {
				t.Errorf("safeVolumeArchiveName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestResolveVolumeArchiveIgnoresUnsafeManifestName proves the guard is wired
// in: an unsafe manifest name is dropped and resolution continues with the
// stable key rather than opening the named path.
func TestResolveVolumeArchiveIgnoresUnsafeManifestName(t *testing.T) {
	t.Parallel()

	const source = "/mnt/user/appdata/plex"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, volumeArchiveBase(source)), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file the crafted name points at, one level up from the staging dir.
	outside := filepath.Join(filepath.Dir(dir), "volume_escape.tar")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	got, err := resolveVolumeArchive(dir, "../volume_escape.tar", source, -1)
	if err != nil {
		t.Fatalf("resolveVolumeArchive() error = %v", err)
	}
	if got != filepath.Join(dir, volumeArchiveBase(source)) {
		t.Errorf("resolveVolumeArchive() = %q, want the stable-key archive inside %q", got, dir)
	}
}

// TestMergeContainerChainRejectsCorruptManifest proves the merge stops on a
// volumes.json it cannot parse. Falling back to filename-only grouping there
// is what silently drops a base full's data when a chain spans the naming
// change, so a corrupt manifest must fail loudly instead.
func TestMergeContainerChainRejectsCorruptManifest(t *testing.T) {
	t.Parallel()

	step := t.TempDir()
	writeStepVolume(t, step, "volume_0.tar", "old.txt")
	if err := os.WriteFile(filepath.Join(step, "volumes.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := MergeContainerChainStaging(context.Background(), []string{step}, t.TempDir())
	if err == nil {
		t.Fatal("expected MergeContainerChainStaging to fail on a corrupt volumes.json")
	}
	if !strings.Contains(err.Error(), "volumes.json") {
		t.Errorf("error %q does not name the offending file", err)
	}
}

// TestLegacyVolumeIndex pins the decision that keeps the legacy archive
// fallback from re-creating the #352 mis-pairing.
func TestLegacyVolumeIndex(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		entry           volumeManifestEntry
		entryFound      bool
		manifestPresent bool
		loopIndex       int
		want            int
	}{
		{
			// The backup-time index, not the current loop position.
			name:            "matched entry uses its recorded index",
			entry:           volumeManifestEntry{Index: 3},
			entryFound:      true,
			manifestPresent: true,
			loopIndex:       0,
			want:            3,
		},
		{
			name:            "no manifest at all falls back to the loop position",
			entryFound:      false,
			manifestPresent: false,
			loopIndex:       2,
			want:            2,
		},
		{
			// The mount is new or changed since the backup; guessing an index
			// would hand it another volume's archive.
			name:            "manifest present but mount unlisted disables the fallback",
			entryFound:      false,
			manifestPresent: true,
			loopIndex:       2,
			want:            -1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := legacyVolumeIndex(tc.entry, tc.entryFound, tc.manifestPresent, tc.loopIndex); got != tc.want {
				t.Errorf("legacyVolumeIndex() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestTarBaseName covers the compression-suffix normalisation both the merge
// and the restore lookup depend on.
func TestTarBaseName(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		{"volume_0.tar", "volume_0.tar"},
		{"volume_0.tar.gz", "volume_0.tar"},
		{"volume_0.tar.zst", "volume_0.tar"},
		{"image.tar.gz", "image.tar"},
		{"no-suffix", "no-suffix"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := tarBaseName(tc.in); got != tc.want {
				t.Errorf("tarBaseName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

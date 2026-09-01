package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	imagetypes "github.com/moby/moby/api/types/image"
	mounttypes "github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// classicUnchangedItem builds the differential item used by the tests below:
// a reference time plus the parent's per-volume listing, which is what makes
// an existing file count as unchanged rather than new (issue #320).
func classicUnchangedItem(volSrc string, changedSince time.Time, prevPaths []string) BackupItem {
	return BackupItem{
		Name: "test", Type: "container",
		Settings: map[string]any{
			"id":                        "abc123",
			"changed_since":             changedSince.UTC().Format(time.RFC3339),
			"prev_volume_listing_paths": map[string][]string{volSrc: prevPaths},
		},
		Compression: CompressionNone,
	}
}

// TestContainerBackupFlagsUnchanged covers the classic path's issue #326
// signal: a differential run that writes no volume archive, no image, no
// template, and no database dump captured nothing, even though it still wrote
// config.json and reports success.
func TestContainerBackupFlagsUnchanged(t *testing.T) {
	t.Parallel()

	changedSince := time.Now().Add(-1 * time.Hour)
	stale := time.Now().Add(-3 * time.Hour)

	// Container created well before the reference time, so the image is not
	// re-saved either — the same shape as re-running a differential job on an
	// idle container.
	createdAt := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)

	newVolume := func(t *testing.T, mtime time.Time) string {
		t.Helper()
		volSrc := t.TempDir()
		p := filepath.Join(volSrc, "old.txt")
		if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		// The directory's own mtime counts too — a fresh TempDir is "now".
		for _, target := range []string{p, volSrc} {
			if err := os.Chtimes(target, mtime, mtime); err != nil {
				t.Fatal(err)
			}
		}
		return volSrc
	}

	t.Run("nothing changed since the reference", func(t *testing.T) {
		t.Parallel()
		volSrc := newVolume(t, stale)
		mock := newClassicMock(t, false, volSrc, createdAt)
		result, err := (&ContainerHandler{cli: mock}).Backup(
			context.Background(), classicUnchangedItem(volSrc, changedSince, []string{"old.txt"}), t.TempDir(), noopProgress)
		if err != nil {
			t.Fatalf("Backup() error = %v", err)
		}
		if !result.Success {
			t.Fatal("an unchanged item is still a successful backup")
		}
		if unchanged, _ := result.Meta[MetaUnchanged].(bool); !unchanged {
			t.Errorf("Meta[%q] = %v, want true", MetaUnchanged, result.Meta[MetaUnchanged])
		}
	})

	t.Run("a changed volume is reported as backed up", func(t *testing.T) {
		t.Parallel()
		volSrc := newVolume(t, time.Now())
		mock := newClassicMock(t, false, volSrc, createdAt)
		result, err := (&ContainerHandler{cli: mock}).Backup(
			context.Background(), classicUnchangedItem(volSrc, changedSince, []string{"old.txt"}), t.TempDir(), noopProgress)
		if err != nil {
			t.Fatalf("Backup() error = %v", err)
		}
		if _, set := result.Meta[MetaUnchanged]; set {
			t.Errorf("Meta[%q] set for a run that archived a changed volume", MetaUnchanged)
		}
	})

	t.Run("a full backup is never unchanged", func(t *testing.T) {
		t.Parallel()
		volSrc := newVolume(t, stale)
		mock := newClassicMock(t, false, volSrc, createdAt)
		item := BackupItem{
			Name: "test", Type: "container",
			Settings:    map[string]any{"id": "abc123"},
			Compression: CompressionNone,
		}
		result, err := (&ContainerHandler{cli: mock}).Backup(context.Background(), item, t.TempDir(), noopProgress)
		if err != nil {
			t.Fatalf("Backup() error = %v", err)
		}
		if _, set := result.Meta[MetaUnchanged]; set {
			t.Errorf("Meta[%q] set for a full backup, which always captures everything", MetaUnchanged)
		}
	})
}

func TestChunkedManifestUnchanged(t *testing.T) {
	t.Parallel()

	id := func(b byte) dedup.ID {
		var out dedup.ID
		for i := range out {
			out[i] = b
		}
		return out
	}
	manifest := func(files map[string]dedup.ManifestEntry) *dedup.Manifest {
		return &dedup.Manifest{Version: 1, Item: "plex", Files: files}
	}
	vol := containerVolPrefix + "/config"

	base := map[string]dedup.ManifestEntry{
		containerInspectKey:   {Chunks: []dedup.ID{id(0x01)}},
		containerImageMetaKey: {Chunks: []dedup.ID{id(0x02)}},
		vol:                   {Chunks: []dedup.ID{id(0x03)}},
	}
	clone := func(mutate func(map[string]dedup.ManifestEntry)) map[string]dedup.ManifestEntry {
		out := make(map[string]dedup.ManifestEntry, len(base))
		for k, v := range base {
			out[k] = v
		}
		mutate(out)
		return out
	}

	cases := []struct {
		name    string
		current *dedup.Manifest
		parent  *dedup.Manifest
		want    bool
	}{
		{
			name: "identical but for the always-rewritten inspect blob",
			current: manifest(clone(func(m map[string]dedup.ManifestEntry) {
				m[containerInspectKey] = dedup.ManifestEntry{Chunks: []dedup.ID{id(0xff)}}
			})),
			parent: manifest(base),
			want:   true,
		},
		{name: "byte-identical", current: manifest(base), parent: manifest(base), want: true},
		{
			name: "a volume's sub-manifest changed",
			current: manifest(clone(func(m map[string]dedup.ManifestEntry) {
				m[vol] = dedup.ManifestEntry{Chunks: []dedup.ID{id(0x09)}}
			})),
			parent: manifest(base),
			want:   false,
		},
		{
			name: "a volume was added",
			current: manifest(clone(func(m map[string]dedup.ManifestEntry) {
				m[containerVolPrefix+"/transcode"] = dedup.ManifestEntry{Chunks: []dedup.ID{id(0x0a)}}
			})),
			parent: manifest(base),
			want:   false,
		},
		{
			name: "a volume was removed",
			current: manifest(clone(func(m map[string]dedup.ManifestEntry) {
				delete(m, vol)
			})),
			parent: manifest(base),
			want:   false,
		},
		{
			name: "a volume grew a chunk",
			current: manifest(clone(func(m map[string]dedup.ManifestEntry) {
				m[vol] = dedup.ManifestEntry{Chunks: []dedup.ID{id(0x03), id(0x04)}}
			})),
			parent: manifest(base),
			want:   false,
		},
		{
			// A plugin's .plg payload lives outside Files, so a manifest whose
			// only difference is the installer must not read as unchanged.
			name:    "only the plugin installer changed",
			current: &dedup.Manifest{Version: 1, Item: "plex", Files: base, Installer: &dedup.ManifestEntry{Chunks: []dedup.ID{id(0x11)}}},
			parent:  &dedup.Manifest{Version: 1, Item: "plex", Files: base, Installer: &dedup.ManifestEntry{Chunks: []dedup.ID{id(0x12)}}},
			want:    false,
		},
		{
			name:    "the plugin installer is unchanged too",
			current: &dedup.Manifest{Version: 1, Item: "plex", Files: base, Installer: &dedup.ManifestEntry{Chunks: []dedup.ID{id(0x11)}}},
			parent:  &dedup.Manifest{Version: 1, Item: "plex", Files: base, Installer: &dedup.ManifestEntry{Chunks: []dedup.ID{id(0x11)}}},
			want:    true,
		},
		{
			// A manifest written before installers were captured has none; the
			// next run capturing one is a real change.
			name:    "an installer was added",
			current: &dedup.Manifest{Version: 1, Item: "plex", Files: base, Installer: &dedup.ManifestEntry{Chunks: []dedup.ID{id(0x11)}}},
			parent:  manifest(base),
			want:    false,
		},
		{
			name:    "an installer disappeared",
			current: manifest(base),
			parent:  &dedup.Manifest{Version: 1, Item: "plex", Files: base, Installer: &dedup.ManifestEntry{Chunks: []dedup.ID{id(0x11)}}},
			want:    false,
		},
		{name: "no parent to compare against", current: manifest(base), parent: nil, want: false},
		{name: "no current manifest", current: nil, parent: manifest(base), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ChunkedManifestUnchanged(tc.current, tc.parent); got != tc.want {
				t.Errorf("ChunkedManifestUnchanged() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestChunkedManifestUnchangedEndToEnd is the assumption the whole chunked
// half of issue #326 rests on: because chunk and sub-manifest IDs are content
// addresses, two BackupChunked runs over an untouched volume produce manifests
// that differ only in __inspect. A test with hand-built IDs cannot prove that.
func TestChunkedManifestUnchangedEndToEnd(t *testing.T) {
	volSrc := t.TempDir()
	stale := time.Now().Add(-3 * time.Hour)
	payload := filepath.Join(volSrc, "config.yml")
	if err := os.WriteFile(payload, []byte("foo: bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{payload, volSrc} {
		if err := os.Chtimes(target, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	newMock := func() *mockDockerClient {
		return &mockDockerClient{
			inspectResp: client.ContainerInspectResult{
				Container: containertypes.InspectResponse{
					ID:      "deadbeef",
					Name:    "/test-container",
					Created: time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano),
					Config:  &containertypes.Config{Image: "nginx:latest"},
					State:   &containertypes.State{Running: false},
					Mounts: []containertypes.MountPoint{
						{Type: mounttypes.TypeBind, Source: volSrc, Destination: "/etc/myapp"},
					},
				},
			},
			imageResp: client.ImageInspectResult{
				InspectResponse: imagetypes.InspectResponse{
					RepoDigests: []string{"nginx@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
				},
			},
		}
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	backup := func(t *testing.T, item BackupItem, parent *dedup.Manifest) dedup.Manifest {
		t.Helper()
		h := &ContainerHandler{cli: newMock()}
		id, err := h.BackupChunked(context.Background(), item, r, parent, nil)
		if err != nil {
			t.Fatalf("BackupChunked() error = %v", err)
		}
		if err := r.Flush(); err != nil {
			t.Fatalf("Flush() error = %v", err)
		}
		m, err := r.GetManifest(id)
		if err != nil {
			t.Fatalf("GetManifest() error = %v", err)
		}
		return m
	}

	full := backup(t, BackupItem{
		Name: "test-container", Type: "container",
		Settings: map[string]any{"id": "deadbeef"},
	}, nil)

	diffItem := BackupItem{
		Name: "test-container", Type: "container",
		Settings: map[string]any{
			"id":            "deadbeef",
			"changed_since": time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	diff := backup(t, diffItem, &full)
	if !ChunkedManifestUnchanged(&diff, &full) {
		t.Errorf("an untouched volume re-chunked to a different manifest:\ncurrent %v\nparent  %v",
			manifestKeys(diff), manifestKeys(full))
	}

	// Touch the volume and the next run must report as changed.
	if err := os.WriteFile(filepath.Join(volSrc, "new.txt"), []byte("added"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := backup(t, diffItem, &diff)
	if ChunkedManifestUnchanged(&changed, &diff) {
		t.Error("a run that captured a new file reported as unchanged")
	}
}

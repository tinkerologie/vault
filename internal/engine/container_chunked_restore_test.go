package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// backupTestVolume backs up a small source tree as a folder chunked backup
// and returns the sub-manifest ID. The caller owns repo cleanup.
func backupTestVolume(t *testing.T, repo *dedup.Repo, src string) dedup.ID {
	t.Helper()
	fh := &FolderHandler{}
	id, err := fh.BackupChunked(context.Background(), BackupItem{
		Name:     "vol",
		Type:     "folder",
		Settings: map[string]any{"path": src},
	}, repo, nil, nil)
	if err != nil {
		t.Fatalf("BackupChunked() error = %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	return id
}

// inspectFromJSON builds a restoreInspect from a JSON string so tests avoid
// spelling out the anonymous Mounts struct literally.
func inspectFromJSON(t *testing.T, raw string) restoreInspect {
	t.Helper()
	var ins restoreInspect
	if err := json.Unmarshal([]byte(raw), &ins); err != nil {
		t.Fatalf("inspectFromJSON() error = %v", err)
	}
	return ins
}

func TestRestoreChunkedVolumes_CustomDestBindMount(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "config.yml"), []byte("foo: bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	subID := backupTestVolume(t, r, src)

	// Point the mount Source at a path that does NOT exist on disk.
	// With a custom restoreDest the data must land under restoreDest,
	// and the phantom source path must NOT be created.
	phantomSource := filepath.Join(t.TempDir(), "original")
	inspect := inspectFromJSON(t, fmt.Sprintf(`{
		"Name": "/test-container",
		"Config": {"Image": "nginx:latest"},
		"Mounts": [{"Type":"bind","Source":%q,"Destination":"/etc/myapp"}]
	}`, phantomSource))

	restoreDest := t.TempDir()
	m := dedup.Manifest{
		Files: map[string]dedup.ManifestEntry{
			containerVolPrefix + "/etc/myapp": {
				Size:   100,
				Chunks: []dedup.ID{subID},
			},
		},
	}

	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, restoreDest, nil, nil); err != nil {
		t.Fatalf("restoreChunkedVolumes() error = %v", err)
	}

	// Data must be under restoreDest/<base(source)>.
	got := filepath.Join(restoreDest, filepath.Base(phantomSource), "config.yml")
	if _, err := os.Stat(got); err != nil {
		t.Errorf("expected file at %s: %v", got, err)
	}

	// Phantom source path must NOT have been created.
	if _, err := os.Stat(phantomSource); !os.IsNotExist(err) {
		t.Errorf("phantom source %s should not exist after custom-dest restore", phantomSource)
	}
}

func TestRestoreChunkedVolumes_DefaultDestRestoresToSource(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "data.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	subID := backupTestVolume(t, r, src)

	// Target path does not exist yet — restoreChunkedVolumes must create it.
	target := filepath.Join(t.TempDir(), "restore-here")
	inspect := inspectFromJSON(t, fmt.Sprintf(`{
		"Name": "/test-container",
		"Config": {"Image": "nginx:latest"},
		"Mounts": [{"Type":"bind","Source":%q,"Destination":"/data"}]
	}`, target))

	m := dedup.Manifest{
		Files: map[string]dedup.ManifestEntry{
			containerVolPrefix + "/data": {
				Size:   100,
				Chunks: []dedup.ID{subID},
			},
		},
	}

	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, "", nil, nil); err != nil {
		t.Fatalf("restoreChunkedVolumes() error = %v", err)
	}

	got := filepath.Join(target, "data.txt")
	if _, err := os.Stat(got); err != nil {
		t.Errorf("expected file at %s: %v", got, err)
	}
}

func TestRestoreChunkedVolumes_CustomDestNamedVolume(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	subID := backupTestVolume(t, r, src)

	// Named volume: Source is /var/lib/docker/volumes/myvol/_data but the
	// restore target must be restoreDest/myvol (the volume name), not _data.
	inspect := inspectFromJSON(t, `{
		"Name": "/test-container",
		"Config": {"Image": "nginx:latest"},
		"Mounts": [{"Type":"volume","Name":"myvol","Source":"/var/lib/docker/volumes/myvol/_data","Destination":"/data"}]
	}`)

	restoreDest := t.TempDir()
	m := dedup.Manifest{
		Files: map[string]dedup.ManifestEntry{
			containerVolPrefix + "/data": {
				Size:   100,
				Chunks: []dedup.ID{subID},
			},
		},
	}

	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, restoreDest, nil, nil); err != nil {
		t.Fatalf("restoreChunkedVolumes() error = %v", err)
	}

	// Data under restoreDest/myvol/ (NOT restoreDest/_data/).
	got := filepath.Join(restoreDest, "myvol", "index.html")
	if _, err := os.Stat(got); err != nil {
		t.Errorf("expected file at %s: %v", got, err)
	}

	// _data directory must NOT exist.
	if _, err := os.Stat(filepath.Join(restoreDest, "_data")); err == nil {
		t.Errorf("restoreDest/_data should not exist for named-volume restore")
	}
}

func TestRestoreChunkedVolumes_SkippedVolumeNotRestored(t *testing.T) {
	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	target := filepath.Join(t.TempDir(), "should-not-exist")
	inspect := inspectFromJSON(t, fmt.Sprintf(`{
		"Name": "/test-container",
		"Config": {"Image": "nginx:latest"},
		"Mounts": [{"Type":"bind","Source":%q,"Destination":"/cache"}]
	}`, target))

	m := dedup.Manifest{
		Files: map[string]dedup.ManifestEntry{
			containerVolPrefix + "/cache": {
				Size: volumeSkippedSize,
			},
		},
	}

	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, t.TempDir(), nil, nil); err != nil {
		t.Fatalf("restoreChunkedVolumes() error = %v", err)
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("skipped volume target %s should not exist", target)
	}
}

// TestRestoreChunkedVolumes_InvalidMountSource is a regression test for the
// path-traversal fix: a bind-mount Source outside restoreAllowedRoots must
// cause restoreChunkedVolumes to return an "invalid restore path" error before
// os.MkdirAll is ever attempted.
func TestRestoreChunkedVolumes_InvalidMountSource(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "secret.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	subID := backupTestVolume(t, r, src)

	// /dev/vault is outside restoreAllowedRoots — normalizeRestorePath must
	// reject it before os.MkdirAll is called.
	disallowedSource := "/dev/vault"
	inspect := inspectFromJSON(t, fmt.Sprintf(`{
		"Name": "/test-container",
		"Config": {"Image": "nginx:latest"},
		"Mounts": [{"Type":"bind","Source":%q,"Destination":"/data"}]
	}`, disallowedSource))

	m := dedup.Manifest{
		Files: map[string]dedup.ManifestEntry{
			containerVolPrefix + "/data": {
				Size:   100,
				Chunks: []dedup.ID{subID},
			},
		},
	}

	// restoreDest="" causes volumeRestoreTarget to return Source directly,
	// so normalizeRestorePath must reject /dev/vault.
	err := restoreChunkedVolumes(context.Background(), m, r, inspect, "", nil, nil)
	if err == nil {
		t.Fatal("restoreChunkedVolumes() expected error for path outside allowed roots, got nil")
	}
	if !strings.Contains(err.Error(), "invalid restore path") {
		t.Errorf("restoreChunkedVolumes() error = %q, want it to contain \"invalid restore path\"", err.Error())
	}

	// The disallowed path must not have been created on disk.
	if _, statErr := os.Stat(disallowedSource); !os.IsNotExist(statErr) {
		t.Errorf("disallowed path %s should not have been created", disallowedSource)
	}
}

// TestRestoreChunkedVolumes_FilePickerSelection covers issue #275: a restore
// driven by the file picker must extract only the selected paths, and must
// leave volumes the selection never names completely untouched.
func TestRestoreChunkedVolumes_FilePickerSelection(t *testing.T) {
	configSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(configSrc, "wanted.yml"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configSrc, "unwanted.yml"), []byte("drop\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	transcodeSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(transcodeSrc, "tmp.bin"), []byte("scratch"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	configID := backupTestVolume(t, r, configSrc)
	transcodeID := backupTestVolume(t, r, transcodeSrc)

	inspect := inspectFromJSON(t, `{
		"Name": "/test-container",
		"Config": {"Image": "nginx:latest"},
		"Mounts": [
			{"Type":"bind","Source":"/mnt/user/appdata/config","Destination":"/config"},
			{"Type":"bind","Source":"/mnt/user/appdata/transcode","Destination":"/transcode"}
		]
	}`)

	restoreDest := t.TempDir()
	m := dedup.Manifest{
		Files: map[string]dedup.ManifestEntry{
			containerVolPrefix + "/config":    {Size: 100, Chunks: []dedup.ID{configID}},
			containerVolPrefix + "/transcode": {Size: 100, Chunks: []dedup.ID{transcodeID}},
		},
	}

	selection := []string{"/config/wanted.yml"}
	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, restoreDest, selection, nil); err != nil {
		t.Fatalf("restoreChunkedVolumes() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(restoreDest, "config", "wanted.yml")); err != nil {
		t.Errorf("selected file should have been restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(restoreDest, "config", "unwanted.yml")); !os.IsNotExist(err) {
		t.Errorf("unselected file in the selected volume should not exist, stat err = %v", err)
	}
	// The whole /transcode volume is outside the selection, so it must not be
	// touched at all — not even an empty directory.
	if _, err := os.Stat(filepath.Join(restoreDest, "transcode")); !os.IsNotExist(err) {
		t.Errorf("unselected volume should not have been restored, stat err = %v", err)
	}
}

// TestRestoreChunkedVolumes_MountPointSelection picks the mount point itself,
// which means "restore this whole volume".
func TestRestoreChunkedVolumes_MountPointSelection(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.yml"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "b.yml"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	subID := backupTestVolume(t, r, src)
	inspect := inspectFromJSON(t, `{
		"Name": "/test-container",
		"Config": {"Image": "nginx:latest"},
		"Mounts": [{"Type":"bind","Source":"/mnt/user/appdata/config","Destination":"/config"}]
	}`)

	restoreDest := t.TempDir()
	m := dedup.Manifest{
		Files: map[string]dedup.ManifestEntry{
			containerVolPrefix + "/config": {Size: 100, Chunks: []dedup.ID{subID}},
		},
	}

	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, restoreDest, []string{"/config"}, nil); err != nil {
		t.Fatalf("restoreChunkedVolumes() error = %v", err)
	}

	for _, name := range []string{"a.yml", "b.yml"} {
		if _, err := os.Stat(filepath.Join(restoreDest, "config", name)); err != nil {
			t.Errorf("expected %s to be restored: %v", name, err)
		}
	}
}

// TestRestoreChunkedVolumes_NestedMounts covers mounts that nest inside one
// another: a file selected inside the deeper mount must be extracted from that
// mount only, never routed into the parent whose archive never held it, and
// picking the parent's mount point must bring the nested mount with it.
func TestRestoreChunkedVolumes_NestedMounts(t *testing.T) {
	configSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(configSrc, "settings.yml"), []byte("cfg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheSrc, "f.yml"), []byte("cache\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	configID := backupTestVolume(t, r, configSrc)
	cacheID := backupTestVolume(t, r, cacheSrc)

	inspect := inspectFromJSON(t, `{
		"Name": "/test-container",
		"Config": {"Image": "nginx:latest"},
		"Mounts": [
			{"Type":"bind","Source":"/mnt/user/appdata/config","Destination":"/config"},
			{"Type":"bind","Source":"/mnt/user/appdata/cache","Destination":"/config/cache"}
		]
	}`)
	m := dedup.Manifest{
		Files: map[string]dedup.ManifestEntry{
			containerVolPrefix + "/config":       {Size: 100, Chunks: []dedup.ID{configID}},
			containerVolPrefix + "/config/cache": {Size: 100, Chunks: []dedup.ID{cacheID}},
		},
	}

	t.Run("a file in the nested mount comes only from that mount", func(t *testing.T) {
		restoreDest := t.TempDir()
		selection := []string{"/config/cache/f.yml"}
		if err := restoreChunkedVolumes(context.Background(), m, r, inspect, restoreDest, selection, nil); err != nil {
			t.Fatalf("restoreChunkedVolumes() error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(restoreDest, "cache", "f.yml")); err != nil {
			t.Errorf("selected file should have been restored from the nested mount: %v", err)
		}
		// The parent volume never held cache/f.yml, so it must not be touched.
		if _, err := os.Stat(filepath.Join(restoreDest, "config")); !os.IsNotExist(err) {
			t.Errorf("parent mount should not have been restored, stat err = %v", err)
		}
	})

	t.Run("picking the parent mount point covers the nested mount", func(t *testing.T) {
		restoreDest := t.TempDir()
		if err := restoreChunkedVolumes(context.Background(), m, r, inspect, restoreDest, []string{"/config"}, nil); err != nil {
			t.Fatalf("restoreChunkedVolumes() error = %v", err)
		}
		for _, rel := range []string{filepath.Join("config", "settings.yml"), filepath.Join("cache", "f.yml")} {
			if _, err := os.Stat(filepath.Join(restoreDest, rel)); err != nil {
				t.Errorf("expected %s to be restored: %v", rel, err)
			}
		}
	})
}

// The mount's own root directory belongs to the backup as much as its
// contents do. Restoring into a destination that does not exist yet has to
// reproduce it rather than invent a fresh root-owned 0750 directory — a
// container that starts as its own unprivileged user cannot read the latter,
// which is what stopped a container restored into a new folder from starting.
func TestRestoreChunkedVolumes_RestoresVolumeRootMode(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "config.yml"), []byte("foo: bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o775); err != nil {
		t.Fatal(err)
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	subID := backupTestVolume(t, r, src)

	phantomSource := filepath.Join(t.TempDir(), "appdata", "myapp")
	inspect := inspectFromJSON(t, fmt.Sprintf(`{
		"Name": "/test-container",
		"Config": {"Image": "nginx:latest"},
		"Mounts": [{"Type":"bind","Source":%q,"Destination":"/etc/myapp"}]
	}`, phantomSource))

	restoreDest := t.TempDir()
	m := dedup.Manifest{
		Files: map[string]dedup.ManifestEntry{
			containerVolPrefix + "/etc/myapp": volumePointerEntry(src, subID),
		},
	}

	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, restoreDest, nil, nil); err != nil {
		t.Fatalf("restoreChunkedVolumes() error = %v", err)
	}

	root := filepath.Join(restoreDest, filepath.Base(phantomSource))
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("volume root not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o775 {
		t.Errorf("volume root mode = %04o, want 0775", got)
	}
}

// A manifest written before root metadata existed records no mode, and the
// restore must leave the directory as it created it rather than chmod to 0.
func TestRestoreChunkedVolumes_LegacyEntryLeavesRootAlone(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "config.yml"), []byte("foo: bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	subID := backupTestVolume(t, r, src)

	phantomSource := filepath.Join(t.TempDir(), "appdata", "myapp")
	inspect := inspectFromJSON(t, fmt.Sprintf(`{
		"Name": "/test-container",
		"Config": {"Image": "nginx:latest"},
		"Mounts": [{"Type":"bind","Source":%q,"Destination":"/etc/myapp"}]
	}`, phantomSource))

	restoreDest := t.TempDir()
	m := dedup.Manifest{
		Files: map[string]dedup.ManifestEntry{
			// No Mode, no owner: exactly what a pre-existing backup holds.
			containerVolPrefix + "/etc/myapp": {Size: 100, Chunks: []dedup.ID{subID}},
		},
	}

	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, restoreDest, nil, nil); err != nil {
		t.Fatalf("restoreChunkedVolumes() error = %v", err)
	}

	root := filepath.Join(restoreDest, filepath.Base(phantomSource))
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("volume root not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Errorf("volume root mode = %04o, want the unchanged 0750 the restore creates", got)
	}
}

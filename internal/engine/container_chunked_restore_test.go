package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	}, repo, nil)
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

	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, restoreDest, nil); err != nil {
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

	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, "", nil); err != nil {
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

	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, restoreDest, nil); err != nil {
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

	if err := restoreChunkedVolumes(context.Background(), m, r, inspect, t.TempDir(), nil); err != nil {
		t.Fatalf("restoreChunkedVolumes() error = %v", err)
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("skipped volume target %s should not exist", target)
	}
}

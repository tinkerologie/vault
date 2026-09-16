package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// A single-file bind mount holds its chunks on the container manifest
// directly, because there is no tree to delegate to FolderHandler. These tests
// are the restore half of issue #380: before it, the chunked path handed every
// mount to FolderHandler regardless of what the inode actually was, so a file
// mount was lost and a socket mount produced a walk of nothing.

func TestRestoreChunkedVolumes_SingleFileMount(t *testing.T) {
	const body = "tailscale-hook-contents\n"

	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	// The mount's host path, as recorded at backup time.
	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "hook.sh")
	if err := os.WriteFile(source, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	entry, err := chunkFileIntoRepo(repo, source)
	if err != nil {
		t.Fatal(err)
	}
	entry.Mode = 0o755
	entry.ModTime = time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	// Restore it somewhere else, so the assertion cannot pass by accident.
	dest := t.TempDir()
	m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		containerVolFilePrefix + "/hook.sh": entry,
	}}
	inspect := inspectFromJSON(t, fmt.Sprintf(`{
		"Name": "/ts",
		"Mounts": [{"Type": "bind", "Source": %q, "Destination": "/hook.sh"}]
	}`, source))

	if err := restoreChunkedVolumes(context.Background(), m, repo, inspect, dest, nil, false, nil); err != nil {
		t.Fatalf("restoreChunkedVolumes: %v", err)
	}

	// With a custom destination the mount lands under it, named after the
	// mount's own last path component.
	restored := filepath.Join(dest, "hook.sh")
	data, err := os.ReadFile(restored)
	if err != nil {
		t.Fatalf("file mount was not restored: %v", err)
	}
	if string(data) != body {
		t.Errorf("restored contents = %q, want %q", data, body)
	}
	info, err := os.Lstat(restored)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("mode = %04o, want 0755 — an executable hook restored 0644 does not run", got)
	}
	wantTime, _ := time.Parse(time.RFC3339, entry.ModTime)
	if got := info.ModTime().UTC().Truncate(time.Second); !got.Equal(wantTime) {
		t.Errorf("mtime = %s, want %s", got, wantTime)
	}
}

// A file mount the picker's selection does not name must be left alone, the
// same rule the volume loop applies (issue #275).
func TestRestoreChunkedVolumes_SingleFileMountSelection(t *testing.T) {
	cases := []struct {
		name      string
		selection []string
		want      bool
	}{
		{name: "no selection restores everything", want: true},
		{name: "the file is selected", selection: []string{"/hook.sh"}, want: true},
		{name: "another path is selected", selection: []string{"/config/other.yml"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, _, cleanup := dedup.NewTestRepoForEngine(t)
			defer cleanup()

			source := filepath.Join(t.TempDir(), "hook.sh")
			if err := os.WriteFile(source, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			entry, err := chunkFileIntoRepo(repo, source)
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.Flush(); err != nil {
				t.Fatal(err)
			}

			dest := t.TempDir()
			m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
				containerVolFilePrefix + "/hook.sh": entry,
			}}
			inspect := inspectFromJSON(t, fmt.Sprintf(`{
				"Name": "/ts",
				"Mounts": [{"Type": "bind", "Source": %q, "Destination": "/hook.sh"}]
			}`, source))

			if err := restoreChunkedVolumes(context.Background(), m, repo, inspect, dest, tc.selection, false, nil); err != nil {
				t.Fatalf("restoreChunkedVolumes: %v", err)
			}
			_, statErr := os.Stat(filepath.Join(dest, "hook.sh"))
			if tc.want && statErr != nil {
				t.Errorf("the file mount should have been restored: %v", statErr)
			}
			if !tc.want && statErr == nil {
				t.Error("an unselected file mount should not have been restored")
			}
		})
	}
}

// The GC mark phase has to see a file mount's chunks as reachable data. They
// are NOT sub-manifests, and the __volfile__ prefix does not match __vol__, so
// the walker must classify them as data rather than trying to recurse.
func TestWalkManifestClosureTreatsFileMountChunksAsData(t *testing.T) {
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	source := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(source, []byte("reachable"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry, err := chunkFileIntoRepo(repo, source)
	if err != nil {
		t.Fatal(err)
	}
	topID, err := repo.PutManifest("ts", dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		containerVolFilePrefix + "/hook.sh": entry,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	_, data, err := WalkManifestClosure(repo, []dedup.ID{topID})
	if err != nil {
		t.Fatalf("WalkManifestClosure: %v", err)
	}
	for _, want := range entry.Chunks {
		found := false
		for _, got := range data {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("chunk %x was not reported as reachable data — GC would sweep it", want[:8])
		}
	}
}

func TestContainerVolumeFileDest(t *testing.T) {
	t.Parallel()

	if dest, ok := ContainerVolumeFileDest("__volfile__/hook.sh"); !ok || dest != "/hook.sh" {
		t.Errorf("ContainerVolumeFileDest() = %q, %v", dest, ok)
	}
	// A volume pointer must not be mistaken for a file mount, and vice versa.
	if _, ok := ContainerVolumeFileDest("__vol__/config"); ok {
		t.Error("a __vol__ key is not a file mount")
	}
	if _, ok := ContainerVolumeDest("__volfile__/hook.sh"); ok {
		t.Error("a __volfile__ key is not a volume pointer")
	}
	// It carries no restorable path of its own, so it must stay out of the
	// synthetic-key set — that set is what the picker filters away.
	if IsSyntheticContainerKey("__volfile__/hook.sh") {
		t.Error("a file mount is restorable content, not engine metadata")
	}
	if !IsSyntheticContainerKey(containerTemplateKey) {
		t.Error("the template is engine metadata and must not appear in a picker")
	}
}

// Picking a parent directory in the file picker must bring the single-file
// mounts nested under it along — the picker draws them as one tree, so a
// membership test against the exact path would silently drop them.
func TestRestoreChunkedVolumes_FileMountSelectedByParent(t *testing.T) {
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	source := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(source, []byte("k=v"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry, err := chunkFileIntoRepo(repo, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		containerVolFilePrefix + "/config/app.conf": entry,
	}}
	inspect := inspectFromJSON(t, fmt.Sprintf(`{
		"Name": "/ts",
		"Mounts": [{"Type": "bind", "Source": %q, "Destination": "/config/app.conf"}]
	}`, source))

	if err := restoreChunkedVolumes(context.Background(), m, repo, inspect, dest, []string{"/config"}, false, nil); err != nil {
		t.Fatalf("restoreChunkedVolumes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "app.conf")); err != nil {
		t.Errorf("picking /config should have restored the nested file mount: %v", err)
	}
}

// A pre-existing symlink at the restore target must not be followed: the
// parent is validated, but the final component could point anywhere on the
// host and the write would escape the approved roots (CWE-59).
func TestRestoreChunkedVolumeFileRefusesSymlinkTarget(t *testing.T) {
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	source := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(source, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry, err := chunkFileIntoRepo(repo, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "precious")
	if err := os.WriteFile(victim, []byte("do not touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "app.conf")
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}

	err = restoreChunkedVolumeFile(repo, entry, target)
	if err == nil {
		t.Fatal("restoring through a symlink should be refused")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error %q should say the symlink was the reason", err)
	}
	data, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "do not touch" {
		t.Errorf("the symlink's target was overwritten: %q", data)
	}
}

// The file-mount loop's defensive paths. Each one is a manifest or inspect
// that disagrees with the other; the restore must skip or report, never
// silently write the wrong thing.
func TestRestoreChunkedVolumes_FileMountEdgeCases(t *testing.T) {
	newRepoWithFile := func(t *testing.T, body string) (*dedup.Repo, dedup.ManifestEntry, string) {
		t.Helper()
		repo, _, cleanup := dedup.NewTestRepoForEngine(t)
		t.Cleanup(cleanup)
		source := filepath.Join(t.TempDir(), "hook.sh")
		if err := os.WriteFile(source, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		entry, err := chunkFileIntoRepo(repo, source)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Flush(); err != nil {
			t.Fatal(err)
		}
		return repo, entry, source
	}

	t.Run("an entry with no chunks is skipped", func(t *testing.T) {
		repo, _, source := newRepoWithFile(t, "x")
		dest := t.TempDir()
		m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
			containerVolFilePrefix + "/hook.sh": {Size: 12},
		}}
		inspect := inspectFromJSON(t, fmt.Sprintf(`{
			"Name": "/ts",
			"Mounts": [{"Type": "bind", "Source": %q, "Destination": "/hook.sh"}]
		}`, source))

		if err := restoreChunkedVolumes(context.Background(), m, repo, inspect, dest, nil, false, nil); err != nil {
			t.Fatalf("a chunkless entry must be skipped, not fatal: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(dest, "hook.sh")); !os.IsNotExist(err) {
			t.Errorf("an empty file was materialised for a chunkless entry: %v", err)
		}
	})

	t.Run("a mount the inspect no longer has is skipped", func(t *testing.T) {
		repo, entry, source := newRepoWithFile(t, "x")
		dest := t.TempDir()
		m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
			// Backed up when the container still mounted it; the mount has
			// since been removed from the container definition.
			containerVolFilePrefix + "/gone.sh": entry,
		}}
		inspect := inspectFromJSON(t, fmt.Sprintf(`{
			"Name": "/ts",
			"Mounts": [{"Type": "bind", "Source": %q, "Destination": "/hook.sh"}]
		}`, source))

		if err := restoreChunkedVolumes(context.Background(), m, repo, inspect, dest, nil, false, nil); err != nil {
			t.Fatalf("a mount missing from the inspect must be skipped, not fatal: %v", err)
		}
		if entries, err := os.ReadDir(dest); err != nil || len(entries) != 0 {
			t.Errorf("destination should be untouched, got %v (err %v)", entries, err)
		}
	})

	t.Run("an unresolvable target is refused before anything is written", func(t *testing.T) {
		repo, entry, _ := newRepoWithFile(t, "x")
		m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
			containerVolFilePrefix + "/hook.sh": entry,
		}}
		// A mount source of "/" has no usable last component, so a remapped
		// target cannot be derived for it. The collision preflight resolves
		// every mount up front, so this is rejected before the file-mount
		// loop runs at all — which is the behaviour we want pinned: a
		// container whose mounts cannot all be remapped is not restored
		// halfway.
		inspect := inspectFromJSON(t, `{
			"Name": "/ts",
			"Mounts": [{"Type": "bind", "Source": "/", "Destination": "/hook.sh"}]
		}`)

		dest := t.TempDir()
		err := restoreChunkedVolumes(context.Background(), m, repo, inspect, dest, nil, false, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid restore name") {
			t.Fatalf("error = %v, want the unresolvable mount name to be reported", err)
		}
		if entries, readErr := os.ReadDir(dest); readErr != nil || len(entries) != 0 {
			t.Errorf("destination should be untouched, got %v (err %v)", entries, readErr)
		}
	})

	t.Run("a failed write is reported", func(t *testing.T) {
		repo, entry, source := newRepoWithFile(t, "x")
		// A directory already occupies the restore target, so the open fails.
		dest := t.TempDir()
		if err := os.Mkdir(filepath.Join(dest, "hook.sh"), 0o750); err != nil {
			t.Fatal(err)
		}
		m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
			containerVolFilePrefix + "/hook.sh": entry,
		}}
		inspect := inspectFromJSON(t, fmt.Sprintf(`{
			"Name": "/ts",
			"Mounts": [{"Type": "bind", "Source": %q, "Destination": "/hook.sh"}]
		}`, source))

		err := restoreChunkedVolumes(context.Background(), m, repo, inspect, dest, nil, false, nil)
		if err == nil || !strings.Contains(err.Error(), "restore volume file /hook.sh") {
			t.Fatalf("error = %v, want it to name the file mount", err)
		}
	})

	t.Run("progress reports the restored mount", func(t *testing.T) {
		repo, entry, source := newRepoWithFile(t, "x")
		dest := t.TempDir()
		m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
			containerVolFilePrefix + "/hook.sh": entry,
		}}
		inspect := inspectFromJSON(t, fmt.Sprintf(`{
			"Name": "/ts",
			"Mounts": [{"Type": "bind", "Source": %q, "Destination": "/hook.sh"}]
		}`, source))

		var seen []string
		progress := func(name string, _ int, msg string) { seen = append(seen, name+":"+msg) }
		if err := restoreChunkedVolumes(context.Background(), m, repo, inspect, dest, nil, false, progress); err != nil {
			t.Fatalf("restoreChunkedVolumes: %v", err)
		}
		if len(seen) != 1 || !strings.HasPrefix(seen[0], "/hook.sh:") {
			t.Fatalf("progress = %v, want one report naming /hook.sh", seen)
		}
	})
}

// restoreChunkedVolumeFile's own failure modes, driven directly because the
// loop above cannot reach all of them through a valid inspect.
func TestRestoreChunkedVolumeFileFailures(t *testing.T) {
	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	source := filepath.Join(t.TempDir(), "hook.sh")
	if err := os.WriteFile(source, []byte("hook\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry, err := chunkFileIntoRepo(repo, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	t.Run("a target outside the approved roots is rejected", func(t *testing.T) {
		err := restoreChunkedVolumeFile(repo, entry, "/nope/escape/hook.sh")
		if err == nil {
			t.Fatal("a path outside the restore roots must be refused")
		}
	})

	t.Run("an unusable parent directory is reported", func(t *testing.T) {
		// A regular file stands where a parent directory would have to be,
		// so MkdirAll cannot create the tree.
		base := t.TempDir()
		blocker := filepath.Join(base, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := restoreChunkedVolumeFile(repo, entry, filepath.Join(blocker, "sub", "hook.sh"))
		if err == nil {
			t.Fatal("restoring under a file must fail")
		}
	})

	t.Run("a parent directory that cannot be created is reported", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		base := t.TempDir()
		readOnly := filepath.Join(base, "ro")
		if err := os.Mkdir(readOnly, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

		err := restoreChunkedVolumeFile(repo, entry, filepath.Join(readOnly, "sub", "hook.sh"))
		if err == nil || !strings.Contains(err.Error(), "mkdir") {
			t.Fatalf("error = %v, want it to report the directory it could not create", err)
		}
	})

	t.Run("a missing chunk is reported", func(t *testing.T) {
		var missing dedup.ID
		missing[0] = 0x7f
		broken := entry
		broken.Chunks = []dedup.ID{missing}
		target := filepath.Join(t.TempDir(), "hook.sh")

		err := restoreChunkedVolumeFile(repo, broken, target)
		if err == nil || !strings.Contains(err.Error(), "reading chunk") {
			t.Fatalf("error = %v, want it to mention reading a chunk", err)
		}
	})

	t.Run("a zero mode falls back to 0644", func(t *testing.T) {
		bare := entry
		bare.Mode = 0
		bare.ModTime = ""
		target := filepath.Join(t.TempDir(), "hook.sh")
		if err := restoreChunkedVolumeFile(repo, bare, target); err != nil {
			t.Fatalf("restoreChunkedVolumeFile: %v", err)
		}
		info, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("mode = %04o, want 0644 for an entry that recorded none", got)
		}
	})
}

func TestRestoreChunkedVolumes_EmptySingleFileMount(t *testing.T) {
	t.Parallel()

	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "empty.conf")
	if err := os.WriteFile(source, nil, 0o640); err != nil {
		t.Fatal(err)
	}

	entry, err := chunkFileIntoRepo(repo, source)
	if err != nil {
		t.Fatal(err)
	}
	entry.Mode = 0o640
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	m := dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		containerVolFilePrefix + "/empty.conf": entry,
	}}
	inspect := inspectFromJSON(t, fmt.Sprintf(`{
		"Name": "/test-empty",
		"Mounts": [{"Type": "bind", "Source": %q, "Destination": "/empty.conf"}]
	}`, source))

	if err := restoreChunkedVolumes(context.Background(), m, repo, inspect, dest, nil, false, nil); err != nil {
		t.Fatalf("restoreChunkedVolumes: %v", err)
	}

	restored := filepath.Join(dest, "empty.conf")
	info, err := os.Stat(restored)
	if err != nil {
		t.Fatalf("restored file does not exist: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("restored file size = %d, want 0", info.Size())
	}
}



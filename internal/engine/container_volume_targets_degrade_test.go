package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A volume whose restore target cannot be resolved is excluded from the
// result rather than guessed at: the caller prunes against these paths, so a
// wrong one would delete files the restore never wrote.
func TestContainerVolumeTargetsExcludesUnresolvableVolumes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		source      string
		restoreDest string
	}{
		{
			// With an alternate destination the target is built from the
			// mount's last path component, and ".." is not a usable one.
			name:        "source with no usable name component",
			source:      "/mnt/..",
			restoreDest: "/mnt/user/restored",
		},
		{
			// Restored in place, so the target is the mount source itself —
			// which here sits outside every approved restore root.
			name:   "source outside the approved restore roots",
			source: "/nowhere/appdata",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			config, err := json.Marshal(map[string]any{
				"Name":   "/test",
				"Mounts": []map[string]any{{"Type": "bind", "Source": tc.source, "Destination": "/data"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := json.Marshal([]map[string]any{{
				"index": 0, "source": tc.source, "destination": "/data",
				"backed_up": true, "archive": "volume_abcdef0123456789.tar",
			}})
			if err != nil {
				t.Fatal(err)
			}
			writeTargetsFixture(t, dir, string(config), string(manifest))

			targets, err := ContainerVolumeTargets(dir, tc.restoreDest)
			if err != nil {
				t.Fatalf("an unresolvable volume is skipped, not an error: %v", err)
			}
			if len(targets) != 0 {
				t.Errorf("expected no targets, got %+v", targets)
			}
		})
	}
}

// An unreadable manifest must stay distinct from an absent one: absent means
// "no prune is possible", unreadable means "something is wrong", and only the
// latter is an error.
func TestContainerVolumeTargetsUnreadableManifest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	config, err := json.Marshal(map[string]any{
		"Name":   "/test",
		"Mounts": []map[string]any{{"Type": "bind", "Source": "/mnt/user/appdata", "Destination": "/data"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeTargetsFixture(t, dir, string(config), "")

	// A directory where volumes.json belongs: present, but no read can
	// succeed — so this is neither "absent" nor "parsed".
	if err := os.Mkdir(filepath.Join(dir, "volumes.json"), 0o750); err != nil {
		t.Fatal(err)
	}

	if _, err := ContainerVolumeTargets(dir, ""); err == nil {
		t.Fatal("an unreadable volumes.json must be reported, not treated as absent")
	}
	if _, err := ContainerVolumeArchives(dir); err == nil {
		t.Fatal("an unreadable volumes.json must be reported to the archive lookup too")
	}
}

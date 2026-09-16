package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTargetsFixture writes a staged container restore point's config.json
// and (optionally) its volumes.json into dir.
func writeTargetsFixture(t *testing.T, dir, config, manifest string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, "volumes.json"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const targetsConfig = `{"Name":"/app","Mounts":[
	{"Type":"bind","Source":"/mnt/user/appdata/app","Destination":"/data"},
	{"Type":"volume","Name":"appvol","Source":"/mnt/user/docker-volumes/appvol/_data","Destination":"/var/lib/app"},
	{"Type":"tmpfs","Source":"/nope","Destination":"/tmp"},
	{"Type":"bind","Source":"/mnt/user/appdata/unlisted","Destination":"/extra"}
]}`

const targetsManifest = `[
	{"index":0,"source":"/mnt/user/appdata/app","destination":"/data","backed_up":true,"archive":"volume_aa11.tar"},
	{"index":1,"source":"/mnt/user/docker-volumes/appvol/_data","destination":"/var/lib/app","backed_up":true,"archive":"volume_bb22.tar"}
]`

// TestContainerVolumeTargets pins that the helper resolves the same targets
// ContainerHandler.Restore writes to — including the named-volume rewrite —
// and skips mounts that are not backupable or not in the manifest.
func TestContainerVolumeTargets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		restoreDest string
		want        map[string]string // source -> target
	}{
		{
			name:        "no override restores over the original sources",
			restoreDest: "",
			want: map[string]string{
				"/mnt/user/appdata/app":                 "/mnt/user/appdata/app",
				"/mnt/user/docker-volumes/appvol/_data": "/mnt/user/docker-volumes/appvol/_data",
			},
		},
		{
			// A named volume lands under its NAME, not under "_data", so the
			// data agrees with the bind the recreated container gets.
			name:        "an alternate destination keys named volumes on the volume name",
			restoreDest: "/mnt/user/restores/app",
			want: map[string]string{
				"/mnt/user/appdata/app":                 "/mnt/user/restores/app/app",
				"/mnt/user/docker-volumes/appvol/_data": "/mnt/user/restores/app/appvol",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeTargetsFixture(t, dir, targetsConfig, targetsManifest)

			got, err := ContainerVolumeTargets(dir, tc.restoreDest)
			if err != nil {
				t.Fatalf("ContainerVolumeTargets() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d targets, want %d: %+v", len(got), len(tc.want), got)
			}
			for _, vt := range got {
				want, ok := tc.want[vt.Source]
				if !ok {
					t.Fatalf("unexpected volume %q", vt.Source)
				}
				if vt.Target != want {
					t.Errorf("volume %s target = %q, want %q", vt.Source, vt.Target, want)
				}
				if !vt.BackedUp {
					t.Errorf("volume %s should be marked backed up", vt.Source)
				}
			}
		})
	}
}

// TestContainerVolumeTargetsRefusesUnusableInput pins that every input the
// helper cannot trust yields no targets rather than a guess — the prune must
// never operate on paths it is not certain the restore wrote.
func TestContainerVolumeTargetsRefusesUnusableInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		config   string
		manifest string
		dest     string
		wantErr  bool
	}{
		{name: "missing config", config: "", manifest: targetsManifest, wantErr: true},
		{name: "corrupt config", config: `{"Mounts":`, manifest: targetsManifest, wantErr: true},
		{name: "corrupt manifest", config: targetsConfig, manifest: `{"volumes":[]}`, wantErr: true},
		{name: "destination outside the approved roots", config: targetsConfig, manifest: targetsManifest, dest: "/nope/elsewhere", wantErr: true},
		{name: "absent manifest yields no targets", config: targetsConfig, manifest: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeTargetsFixture(t, dir, tc.config, tc.manifest)

			got, err := ContainerVolumeTargets(dir, tc.dest)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ContainerVolumeTargets() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ContainerVolumeTargets() error = %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("got %d targets, want none: %+v", len(got), got)
			}
		})
	}
}

// TestContainerVolumeArchives pins that a step is asked what IT called each
// volume, and that names it cannot vouch for are dropped rather than joined
// onto the step directory.
func TestContainerVolumeArchives(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		manifest string
		want     map[string]string
		wantErr  bool
	}{
		{
			name:     "backed-up volumes map to their recorded archive",
			manifest: targetsManifest,
			want: map[string]string{
				"/mnt/user/appdata/app":                 "volume_aa11.tar",
				"/mnt/user/docker-volumes/appvol/_data": "volume_bb22.tar",
			},
		},
		{
			name: "skipped volumes, sourceless entries and unsafe names are dropped",
			manifest: `[
				{"index":0,"source":"/a","backed_up":false,"archive":"volume_aa11.tar"},
				{"index":1,"source":"","backed_up":true,"archive":"volume_bb22.tar"},
				{"index":2,"source":"/c","backed_up":true,"archive":"../../etc/passwd"},
				{"index":3,"source":"/d","backed_up":true,"archive":"volume_dd44.tar.zst"}
			]`,
			want: map[string]string{"/d": "volume_dd44.tar.zst"},
		},
		{name: "absent manifest yields no mapping", manifest: ""},
		{name: "corrupt manifest is an error", manifest: `not json`, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeTargetsFixture(t, dir, "", tc.manifest)

			got, err := ContainerVolumeArchives(dir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ContainerVolumeArchives() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ContainerVolumeArchives() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for src, archive := range tc.want {
				if got[src] != archive {
					t.Errorf("archive for %s = %q, want %q", src, got[src], archive)
				}
			}
		})
	}
}

// TestReadVolumeManifest pins the three states a caller must tell apart:
// present, absent, and unreadable. Treating "unreadable" as "absent" would
// silently re-enable the index-based fallbacks that issue #352 removed.
func TestReadVolumeManifest(t *testing.T) {
	t.Parallel()

	t.Run("present", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeTargetsFixture(t, dir, "", targetsManifest)
		manifest, present, err := readVolumeManifest(dir)
		if err != nil || !present {
			t.Fatalf("readVolumeManifest() = present %v, err %v", present, err)
		}
		if len(manifest) != 2 {
			t.Fatalf("got %d entries, want 2", len(manifest))
		}
	})

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		_, present, err := readVolumeManifest(t.TempDir())
		if err != nil {
			t.Fatalf("readVolumeManifest() error = %v", err)
		}
		if present {
			t.Fatal("readVolumeManifest() reported a manifest that is not there")
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeTargetsFixture(t, dir, "", `[{"index":`)
		if _, _, err := readVolumeManifest(dir); err == nil {
			t.Fatal("readVolumeManifest() error = nil, want an error for a corrupt manifest")
		}
	})
}

package engine

import (
	"reflect"
	"testing"
)

func TestContainerVolumeIncludes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		selection []string
		dest      string
		allDests  []string
		want      []string
		wantVol   bool
	}{
		{
			// No picker filter at all: the pre-#275 whole-volume restore.
			name: "an empty selection restores everything", dest: "/config", wantVol: true,
		},
		{
			name:      "a file inside the volume",
			selection: []string{"/config/settings.yml"},
			dest:      "/config",
			want:      []string{"settings.yml"},
			wantVol:   true,
		},
		{
			name:      "a nested file inside the volume",
			selection: []string{"/config/logs/plex.log"},
			dest:      "/config",
			want:      []string{"logs/plex.log"},
			wantVol:   true,
		},
		{
			// Everything selected lives in a different mount, so extracting
			// this one at all is the issue #275 bug.
			name:      "nothing in this volume is selected",
			selection: []string{"/transcode/tmp.bin"},
			dest:      "/config",
			wantVol:   false,
		},
		{
			name:      "only the paths belonging to this volume are mapped",
			selection: []string{"/config/a.yml", "/transcode/tmp.bin", "/config/b.yml"},
			dest:      "/config",
			want:      []string{"a.yml", "b.yml"},
			wantVol:   true,
		},
		{
			// Picking the mount point means the whole volume, whatever else
			// the selection contains.
			name:      "the mount point itself",
			selection: []string{"/config", "/config/a.yml"},
			dest:      "/config",
			wantVol:   true,
		},
		{
			// A prefix match must respect path boundaries: /configuration is
			// a different mount from /config.
			name:      "a sibling mount with a shared prefix",
			selection: []string{"/configuration/a.yml"},
			dest:      "/config",
			wantVol:   false,
		},
		{
			name:      "trailing and duplicated slashes are normalised away",
			selection: []string{"/config//logs/", "config/b.yml"},
			dest:      "/config/",
			want:      []string{"logs", "b.yml"},
			wantVol:   true,
		},
		{
			name:      "a root mount takes every selected path",
			selection: []string{"/etc/hosts"},
			dest:      "/",
			want:      []string{"etc/hosts"},
			wantVol:   true,
		},
		{
			// A single-file bind mount: the destination is the file.
			name:      "a file mount selected by its own path",
			selection: []string{"/etc/localtime"},
			dest:      "/etc/localtime",
			wantVol:   true,
		},
		{
			name:      "empty strings in the selection are ignored",
			selection: []string{"", "   "},
			dest:      "/config",
			wantVol:   false,
		},
		{
			// Nested mounts: /config/cache is its own volume, so a file inside
			// it belongs to that volume and not to its parent.
			name:      "a file in a nested mount does not leak into the parent",
			selection: []string{"/config/cache/f.yml"},
			dest:      "/config",
			allDests:  []string{"/config", "/config/cache"},
			wantVol:   false,
		},
		{
			name:      "a file in a nested mount routes to the deepest mount",
			selection: []string{"/config/cache/f.yml"},
			dest:      "/config/cache",
			allDests:  []string{"/config", "/config/cache"},
			want:      []string{"f.yml"},
			wantVol:   true,
		},
		{
			// Picking a directory picks what is under it, including whole
			// mounts nested beneath.
			name:      "picking a parent mount point covers a nested mount",
			selection: []string{"/config"},
			dest:      "/config/cache",
			allDests:  []string{"/config", "/config/cache"},
			wantVol:   true,
		},
		{
			name:      "picking a plain directory covers a mount nested under it",
			selection: []string{"/config/sub"},
			dest:      "/config/sub/cache",
			allDests:  []string{"/config", "/config/sub/cache"},
			wantVol:   true,
		},
		{
			// Without a mount list the caller behaves as it did before nesting
			// was considered: the path routes to the mount in hand.
			name:      "no mount list falls back to the mount in hand",
			selection: []string{"/config/cache/f.yml"},
			dest:      "/config",
			want:      []string{"cache/f.yml"},
			wantVol:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, wanted := containerVolumeIncludes(tc.selection, tc.dest, tc.allDests)
			if wanted != tc.wantVol {
				t.Fatalf("volume wanted = %v, want %v", wanted, tc.wantVol)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("includes = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestContainerVolumePath(t *testing.T) {
	t.Parallel()

	cases := []struct{ dest, rel, want string }{
		{"/config", "settings.yml", "/config/settings.yml"},
		{"/config", "logs/plex.log", "/config/logs/plex.log"},
		{"config", "a.yml", "/config/a.yml"},
		{"/config/", "/a.yml", "/config/a.yml"},
		// A file mount: no relative part, the destination is the file.
		{"/etc/localtime", "", "/etc/localtime"},
		{"/", "etc/hosts", "/etc/hosts"},
	}
	for _, tc := range cases {
		if got := ContainerVolumePath(tc.dest, tc.rel); got != tc.want {
			t.Errorf("ContainerVolumePath(%q, %q) = %q, want %q", tc.dest, tc.rel, got, tc.want)
		}
	}
}

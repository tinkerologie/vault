package config

import (
	"slices"
	"testing"
)

func TestGetExclusionPreset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		image     string
		wantMatch bool
		wantKey   string
	}{
		{"linuxserver plex", "lscr.io/linuxserver/plex:latest", true, "plex"},
		{"plexinc image", "plexinc/pms-docker:1.32", true, "plex"},
		{"sonarr", "linuxserver/sonarr:4", true, "sonarr"},
		{"case insensitive", "lscr.io/linuxserver/Sonarr:latest", true, "sonarr"},
		{"unknown image", "mycompany/custom-app:v1", false, ""},
		{"empty image", "", false, ""},
		{"jellyseerr matches seerr", "fallenbagel/jellyseerr:latest", true, "seerr"},
		{"home-assistant", "ghcr.io/home-assistant/home-assistant:stable", true, "home-assistant"},
		// Host-mount monitoring agents (issue #70) — these MUST surface
		// recommended exclusions so users don't run into recursive host
		// filesystem walks that hang the backup job.
		{"telegraf", "telegraf:latest", true, "telegraf"},
		{"glances", "nicolargo/glances:latest", true, "glances"},
		{"netdata", "netdata/netdata:stable", true, "netdata"},
		{"cadvisor", "gcr.io/cadvisor/cadvisor:v0.49.1", true, "cadvisor"},
		{"node-exporter", "prom/node-exporter:v1.7.0", true, "node-exporter"},
		{"scrutiny", "ghcr.io/analogj/scrutiny:master-omnibus", true, "scrutiny"},
		// Docker socket consumers.
		{"watchtower", "containrrr/watchtower:latest", true, "watchtower"},
		{"dozzle", "amir20/dozzle:latest", true, "dozzle"},
		{"dockhand", "ghcr.io/scottyhardy/dockhand:latest", true, "dockhand"},
		// Tdarr server and node images both resolve to the "tdarr" preset via
		// substring matching (issue #188).
		{"tdarr server", "ghcr.io/haveagitgat/tdarr:latest", true, "tdarr"},
		{"tdarr node", "ghcr.io/haveagitgat/tdarr_node:latest", true, "tdarr"},
		// Immich: the official image and the imagegenius fork both resolve to
		// the single "immich" preset via substring matching (issue #187).
		{"immich official", "ghcr.io/immich-app/immich-server:release", true, "immich"},
		{"immich imagegenius", "ghcr.io/imagegenius/immich:latest", true, "immich"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			paths := GetExclusionPreset(tt.image)
			if tt.wantMatch {
				// Assert it resolved to the *specific* expected preset, not just
				// any non-empty one — otherwise a substring collision or ordering
				// change that matched the wrong key would slip through.
				if !slices.Equal(paths, ContainerExclusionPresets[tt.wantKey]) {
					t.Errorf("GetExclusionPreset(%q) = %v, want the %q preset %v",
						tt.image, paths, tt.wantKey, ContainerExclusionPresets[tt.wantKey])
				}
			} else if len(paths) > 0 {
				t.Errorf("GetExclusionPreset(%q) returned %v, expected no match", tt.image, paths)
			}
		})
	}
}

func TestImmichPresetCoversBothLayouts(t *testing.T) {
	t.Parallel()
	paths := GetExclusionPreset("ghcr.io/immich-app/immich-server:release")
	got := make(map[string]bool, len(paths))
	for _, p := range paths {
		got[p] = true
	}

	// Derived content for both the official (/data) and imagegenius (/photos)
	// layouts must be excluded.
	for _, want := range []string{"/data/thumbs", "/data/encoded-video", "/photos/thumbs", "/photos/encoded-video"} {
		if !got[want] {
			t.Errorf("immich preset missing derived-content exclusion %q", want)
		}
	}

	// Critical, must-keep folders must never be excluded, under either layout.
	for _, critical := range []string{
		"/data/upload", "/data/library", "/data/profile", "/data/backups",
		"/photos/upload", "/photos/library", "/photos/profile", "/photos/backups",
	} {
		if got[critical] {
			t.Errorf("immich preset must not exclude critical path %q", critical)
		}
	}
}

func TestGetPresetMeta(t *testing.T) {
	t.Parallel()

	meta, ok := GetPresetMeta("ghcr.io/imagegenius/immich:latest")
	if !ok {
		t.Fatal("expected metadata for immich image")
	}
	if len(meta.Warnings) == 0 {
		t.Error("expected immich metadata to carry a database warning")
	}
	if len(meta.Notes) == 0 {
		t.Error("expected immich metadata to carry explanatory notes")
	}

	if _, ok := GetPresetMeta("linuxserver/sonarr:latest"); ok {
		t.Error("expected no metadata for an image without advisory notes")
	}
	if _, ok := GetPresetMeta(""); ok {
		t.Error("expected no metadata for empty image")
	}
}

func TestAllPresetsNonEmpty(t *testing.T) {
	t.Parallel()
	for key, paths := range ContainerExclusionPresets {
		if len(paths) == 0 {
			t.Errorf("preset %q has empty exclusion paths", key)
		}
		for _, p := range paths {
			if p == "" {
				t.Errorf("preset %q contains empty string", key)
			}
		}
	}
}

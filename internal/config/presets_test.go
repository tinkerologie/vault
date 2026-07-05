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

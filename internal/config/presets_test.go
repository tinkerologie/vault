package config

import (
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			paths := GetExclusionPreset(tt.image)
			if tt.wantMatch && len(paths) == 0 {
				t.Errorf("GetExclusionPreset(%q) returned empty, expected match for key %q", tt.image, tt.wantKey)
			}
			if !tt.wantMatch && len(paths) > 0 {
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

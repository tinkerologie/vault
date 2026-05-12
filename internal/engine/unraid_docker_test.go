package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureUnraidImageTag(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"single name no tag", "influxdb", "library/influxdb:latest"},
		{"single name tagged", "influxdb:1.8", "library/influxdb:1.8"},
		{"user/name no tag", "grafana/grafana", "grafana/grafana:latest"},
		{"user/name tagged", "nicolargo/glances:latest", "nicolargo/glances:latest"},
		{"registry/user/name no tag", "lscr.io/linuxserver/plex", "lscr.io/linuxserver/plex:latest"},
		{"registry/user/name tagged", "lscr.io/linuxserver/plex:1.32", "lscr.io/linuxserver/plex:1.32"},
		{"digest stripped", "nicolargo/glances@sha256:abc", "nicolargo/glances:latest"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ensureUnraidImageTag(tc.in)
			if got != tc.want {
				t.Errorf("ensureUnraidImageTag(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractSHA(t *testing.T) {
	if got := extractSHA("nicolargo/glances@sha256:abc123"); got != "sha256:abc123" {
		t.Errorf("got %q", got)
	}
	if got := extractSHA("nicolargo/glances:latest"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestRestoreUnraidUpdateStatus(t *testing.T) {
	// We cannot write to the fixed `/var/lib/docker/...` path during tests,
	// but we can still validate the no-op behaviour for missing files:
	// the function must not panic and must not create the status file when
	// it doesn't exist on the test host.
	tmp := t.TempDir()

	// Case 1: no image_meta.json — silent return.
	restoreUnraidUpdateStatus(tmp, "nicolargo/glances:latest")

	// Case 2: image_meta.json with empty digests — silent return.
	meta := imageMeta{ImageTag: "nicolargo/glances:latest"}
	mb, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(tmp, "image_meta.json"), mb, 0o600); err != nil {
		t.Fatal(err)
	}
	restoreUnraidUpdateStatus(tmp, "nicolargo/glances:latest")

	// Case 3: image_meta.json with digests — would write if status file
	// existed. On non-Unraid hosts (CI/dev) the status file is missing so
	// nothing happens. We just assert no panic.
	meta = imageMeta{
		ImageTag:    "nicolargo/glances:latest",
		RepoDigests: []string{"nicolargo/glances@sha256:abc123def4567890"},
	}
	mb, _ = json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(tmp, "image_meta.json"), mb, 0o600); err != nil {
		t.Fatal(err)
	}
	restoreUnraidUpdateStatus(tmp, "nicolargo/glances:latest")
}

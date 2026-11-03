//go:build linux

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/dedup"
)

// Issue #545: plugin names and the path setting must not reach arbitrary trees.
func TestPluginPathPolicy(t *testing.T) {
	t.Parallel()
	h := &PluginHandler{}
	for _, name := range []string{"../../etc", "a/b", "..", "."} {
		if _, err := h.BackupChunked(t.Context(), BackupItem{Name: name, Settings: map[string]any{}}, nil, nil, noopProgress); err == nil || !strings.Contains(err.Error(), "invalid plugin name") {
			t.Errorf("name %q: err = %v", name, err)
		}
	}
	if err := h.RestoreChunked(t.Context(), BackupItem{Name: "../../etc", Settings: map[string]any{}}, nil, dedup.ID{}, "", noopProgress); err == nil || !strings.Contains(err.Error(), "invalid plugin name") {
		t.Errorf("restore traversal name: err = %v", err)
	}
	item := BackupItem{Name: "ok", Settings: map[string]any{"path": "/etc/ssh"}}
	if _, err := h.BackupChunked(t.Context(), item, nil, nil, noopProgress); err == nil {
		t.Error("path setting in a system tree allowed")
	}
	if err := h.RestoreChunked(t.Context(), BackupItem{Name: "ok", Settings: map[string]any{"restore_destination": "/etc/x"}}, nil, dedup.ID{}, "/etc/x", noopProgress); err == nil {
		t.Error("custom destination in a system tree allowed")
	}
	if err := h.RestoreChunked(t.Context(), BackupItem{Name: "ok", Settings: map[string]any{}}, nil, dedup.ID{}, "/etc/x", noopProgress); err == nil {
		t.Error("destination in a system tree allowed")
	}
}

// A plugin config directory planted as a symlink must not be archived, by either
// backup path, and a planted installer must not redirect a restore.
func TestPluginDefaultPathsRefuseSymlinks(t *testing.T) {
	base := t.TempDir()
	orig := pluginsDir
	pluginsDir = base
	t.Cleanup(func() { pluginsDir = orig })

	if err := os.Symlink("/etc", filepath.Join(base, "planted")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(base, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	if err := checkPluginConfigDir(filepath.Join(base, "elsewhere")); err == nil {
		t.Error("plugin directory linked to another allowed tree accepted")
	}
	if err := checkPluginConfigDir(filepath.Join(base, "real")); err != nil {
		t.Errorf("plain plugin directory refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "planted.plg"), []byte("<PLUGIN/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &PluginHandler{}
	item := BackupItem{Name: "planted", Settings: map[string]any{}}
	if _, err := h.Backup(t.Context(), item, t.TempDir(), noopProgress); err == nil {
		t.Error("classic backup archived a symlinked config directory")
	}
	if _, err := h.BackupChunked(t.Context(), item, nil, nil, noopProgress); err == nil {
		t.Error("chunked backup archived a symlinked config directory")
	}

	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(base, "evil.plg")); err != nil {
		t.Fatal(err)
	}
	if err := writePluginInstaller("evil", []byte("overwritten")); err == nil {
		t.Error("installer written through a symlink")
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Errorf("victim = %q", got)
	}
	if err := writePluginInstaller("good", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(base, "good.plg")); string(got) != "data" {
		t.Errorf("good.plg = %q", got)
	}
}

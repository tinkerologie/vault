//go:build linux

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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

// The plugins directory itself must not be reachable through a symlink, and a
// symlinked installer is never read into a backup.
func TestPluginsRootAndInstallerRefuseSymlinks(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "plugins")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	orig := pluginsDir
	t.Cleanup(func() { pluginsDir = orig })

	pluginsDir = link
	if _, err := pluginsRoot(); err == nil {
		t.Error("symlinked plugins directory accepted")
	}
	if err := writePluginInstaller("p", []byte("x")); err == nil {
		t.Error("installer written into a symlinked plugins directory")
	}
	if err := checkPluginConfigDir(filepath.Join(link, "p")); err == nil {
		t.Error("config directory under a symlinked plugins directory accepted")
	}

	pluginsDir = real
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(real, "evil.plg")); err != nil {
		t.Fatal(err)
	}
	if _, err := readPluginInstaller("evil"); err == nil {
		t.Error("symlinked installer read")
	}
	if _, err := (&PluginHandler{}).Backup(t.Context(), BackupItem{Name: "evil", Settings: map[string]any{}}, t.TempDir(), noopProgress); err == nil {
		t.Error("classic backup read a symlinked installer")
	}
	if err := os.WriteFile(filepath.Join(real, "good.plg"), []byte("<PLUGIN/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := readPluginInstaller("good"); err != nil || string(got) != "<PLUGIN/>" {
		t.Errorf("readPluginInstaller = %q, %v", got, err)
	}
}

// Issue #595: the chunked plugin backup must read the installer through the
// same pinned, regular-file-only reader as the classic backup. A symlinked,
// FIFO or directory .plg fails the backup, while a missing one stays optional.
func TestPluginChunkedRefusesUnsafeInstaller(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, plg string)
	}{
		{"symlink to a readable file", func(t *testing.T, plg string) {
			secret := filepath.Join(t.TempDir(), "secret")
			if err := os.WriteFile(secret, []byte("top-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(secret, plg); err != nil {
				t.Fatal(err)
			}
		}},
		{"dangling symlink", func(t *testing.T, plg string) {
			if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), plg); err != nil {
				t.Fatal(err)
			}
		}},
		{"fifo", func(t *testing.T, plg string) {
			if err := syscall.Mkfifo(plg, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory", func(t *testing.T, plg string) {
			if err := os.Mkdir(plg, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			orig := pluginsDir
			pluginsDir = base
			t.Cleanup(func() { pluginsDir = orig })
			if err := os.Mkdir(filepath.Join(base, "p"), 0o755); err != nil {
				t.Fatal(err)
			}
			tc.plant(t, filepath.Join(base, "p.plg"))

			r, _, cleanup := dedup.NewTestRepoForEngine(t)
			defer cleanup()
			done := make(chan struct{})
			var id dedup.ID
			var err error
			go func() {
				defer close(done)
				id, err = (&PluginHandler{}).BackupChunked(t.Context(), BackupItem{Name: "p", Settings: map[string]any{}}, r, nil, noopProgress)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("chunked backup blocked on the installer")
			}
			if err == nil {
				t.Fatal("chunked backup accepted an unsafe installer")
			}
			if id != (dedup.ID{}) {
				t.Errorf("manifest ID = %v, want zero", id)
			}
			if strings.Contains(err.Error(), "top-secret") {
				t.Errorf("error leaks the link target: %v", err)
			}
		})
	}
}

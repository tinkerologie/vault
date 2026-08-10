//go:build linux

package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// TestPluginChunkedRoundTrip backs up a synthetic plugin directory (using the
// "path" Settings override so it doesn't depend on /boot/config/plugins/) into
// a dedup repo, restores it to a fresh tempdir, and verifies every regular
// file's bytes match by SHA-256. Exercises the happy path of
// PluginHandler.BackupChunked + RestoreChunked end-to-end on Linux. The
// non-Linux stub returns an "unsupported" error so this test is Linux-only.
func TestPluginChunkedRoundTrip(t *testing.T) {
	src := t.TempDir()
	must := func(p string, data []byte) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(src, "config.toml"), []byte("setting=true"))
	must(filepath.Join(src, "data/state.bin"), bytes.Repeat([]byte{0xee}, 8192))

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &PluginHandler{}
	// Override source via the "path" Settings key (matches what folder uses).
	item := BackupItem{Name: "test-plugin", Type: "plugin", Settings: map[string]any{"path": src}}
	ctx := context.Background()
	manifestID, err := h.BackupChunked(ctx, item, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := h.RestoreChunked(ctx, item, r, manifestID, dst, nil); err != nil {
		t.Fatal(err)
	}

	errs := 0
	_ = filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		a, _ := os.ReadFile(p) // #nosec G304 — test-controlled tempdir
		b, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("missing restored %s: %v", rel, err)
			errs++
			return nil
		}
		if sha256.Sum256(a) != sha256.Sum256(b) {
			t.Errorf("mismatch %s", rel)
			errs++
		}
		return nil
	})
	if errs > 0 {
		t.Fatalf("%d mismatches", errs)
	}
}

// TestPluginChunkedRestoreHonoursFilePicker is a regression test for #271:
// PluginHandler.RestoreChunked dropped restore_file_paths when building the
// proxy folder item, so a partial-restore selection on a dedup plugin backup
// restored the entire config directory instead of only the chosen files.
func TestPluginChunkedRestoreHonoursFilePicker(t *testing.T) {
	src := t.TempDir()
	must := func(p string, data []byte) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(src, "config.toml"), []byte("setting=true"))
	must(filepath.Join(src, "data/state.bin"), bytes.Repeat([]byte{0xee}, 8192))

	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()

	h := &PluginHandler{}
	item := BackupItem{Name: "test-plugin", Type: "plugin", Settings: map[string]any{"path": src}}
	ctx := context.Background()
	manifestID, err := h.BackupChunked(ctx, item, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	restoreItem := BackupItem{
		Name:     "test-plugin",
		Type:     "plugin",
		Settings: map[string]any{"restore_file_paths": []string{"config.toml"}},
	}
	if err := h.RestoreChunked(ctx, restoreItem, r, manifestID, dst, nil); err != nil {
		t.Fatal(err)
	}

	if got, err := os.ReadFile(filepath.Join(dst, "config.toml")); err != nil { // #nosec G304 — test-controlled tempdir
		t.Errorf("selected file config.toml should have been restored: %v", err)
	} else if string(got) != "setting=true" {
		t.Errorf("selected file config.toml has unexpected content: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "data/state.bin")); !os.IsNotExist(err) {
		t.Errorf("unselected file data/state.bin should not have been restored (err=%v)", err)
	}
}

// TestPluginChunkedInstaller is a regression test for #273: the chunked plugin
// backup omitted the .plg installer, so a dedup-only restore left the plugin
// without its installer file. The installer is now recorded out-of-tree in the
// manifest's Installer field and restored to <pluginsDir>/<name>.plg. The
// table covers both an installer-present and an installer-absent (backward
// compatible) plugin.
func TestPluginChunkedInstaller(t *testing.T) {
	const pluginName = "test-plugin"
	plgBody := []byte(`<?xml version="1.0"?><PLUGIN name="test-plugin"></PLUGIN>`)

	cases := []struct {
		name         string
		hasInstaller bool
	}{
		{name: "installer present", hasInstaller: true},
		{name: "installer absent", hasInstaller: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Redirect the well-known plugins dir so backup reads and restore
			// writes the installer under a tempdir instead of /boot/config.
			base := t.TempDir()
			orig := pluginsDir
			pluginsDir = base
			t.Cleanup(func() { pluginsDir = orig })

			src := t.TempDir()
			if err := os.WriteFile(filepath.Join(src, "config.toml"), []byte("setting=true"), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.hasInstaller {
				if err := os.WriteFile(filepath.Join(base, pluginName+".plg"), plgBody, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			r, _, cleanup := dedup.NewTestRepoForEngine(t)
			defer cleanup()

			h := &PluginHandler{}
			item := BackupItem{Name: pluginName, Type: "plugin", Settings: map[string]any{"path": src}}
			ctx := context.Background()
			manifestID, err := h.BackupChunked(ctx, item, r, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Flush(); err != nil {
				t.Fatal(err)
			}

			// Remove the source-side installer so the restore must recreate it.
			_ = os.Remove(filepath.Join(base, pluginName+".plg"))

			dst := t.TempDir()
			if err := h.RestoreChunked(ctx, BackupItem{Name: pluginName, Type: "plugin"}, r, manifestID, dst, nil); err != nil {
				t.Fatal(err)
			}

			if got, err := os.ReadFile(filepath.Join(dst, "config.toml")); err != nil { // #nosec G304 — test-controlled tempdir
				t.Errorf("plugin config should have been restored: %v", err)
			} else if string(got) != "setting=true" {
				t.Errorf("restored config content mismatch: got %q", got)
			}

			plgRestored := filepath.Join(base, pluginName+".plg")
			if tc.hasInstaller {
				if got, err := os.ReadFile(plgRestored); err != nil { // #nosec G304 — test-controlled tempdir
					t.Errorf("plugin .plg should have been restored: %v", err)
				} else if !bytes.Equal(got, plgBody) {
					t.Errorf("restored .plg content mismatch: got %q", got)
				}
			} else if _, err := os.Stat(plgRestored); !os.IsNotExist(err) {
				t.Errorf("no .plg should be restored when none was backed up (err=%v)", err)
			}
		})
	}
}

// TestPluginChunkedRestoreDestination is a regression test for #274:
// PluginHandler.RestoreChunked ignored the restore_destination setting, so
// when the destPath parameter was empty the config always landed in the
// well-known plugin directory. It now falls back to restore_destination,
// mirroring ContainerHandler.RestoreChunked, while an explicit destPath still
// takes precedence.
func TestPluginChunkedRestoreDestination(t *testing.T) {
	base := t.TempDir()
	orig := pluginsDir
	pluginsDir = base
	t.Cleanup(func() { pluginsDir = orig })

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "config.toml"), []byte("x=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	h := &PluginHandler{}
	ctx := context.Background()
	manifestID, err := h.BackupChunked(ctx, BackupItem{Name: "p", Type: "plugin", Settings: map[string]any{"path": src}}, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	t.Run("explicit destPath wins over setting", func(t *testing.T) {
		dst := t.TempDir()
		other := t.TempDir()
		item := BackupItem{Name: "p", Type: "plugin", Settings: map[string]any{"restore_destination": other}}
		if err := h.RestoreChunked(ctx, item, r, manifestID, dst, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dst, "config.toml")); err != nil {
			t.Errorf("config should be restored to the explicit destPath: %v", err)
		}
		if _, err := os.Stat(filepath.Join(other, "config.toml")); !os.IsNotExist(err) {
			t.Errorf("config must not use restore_destination when destPath is set (err=%v)", err)
		}
	})

	t.Run("restore_destination used when destPath empty", func(t *testing.T) {
		dst := t.TempDir()
		item := BackupItem{Name: "p", Type: "plugin", Settings: map[string]any{"restore_destination": dst}}
		if err := h.RestoreChunked(ctx, item, r, manifestID, "", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dst, "config.toml")); err != nil {
			t.Errorf("config should be restored to restore_destination when destPath is empty: %v", err)
		}
	})
}

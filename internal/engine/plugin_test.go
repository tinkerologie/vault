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
	manifestID, err := h.BackupChunked(ctx, item, r, nil, nil)
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
	manifestID, err := h.BackupChunked(ctx, item, r, nil, nil)
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
			manifestID, err := h.BackupChunked(ctx, item, r, nil, nil)
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
	manifestID, err := h.BackupChunked(ctx, BackupItem{Name: "p", Type: "plugin", Settings: map[string]any{"path": src}}, r, nil, nil)
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

// TestPluginListItems tests PluginHandler.ListItems() directly using a temporary directory,
// verifying filtering of hidden and AppleDouble ._* files, and parsing of display names.
func TestPluginListItems(t *testing.T) {
	base := t.TempDir()
	orig := pluginsDir
	pluginsDir = base
	t.Cleanup(func() { pluginsDir = orig })

	// 1. Literal name attribute
	caPLG := `<?xml version="1.0" standalone="yes"?>
<PLUGIN name="Community Applications" version="2026.01.01">
</PLUGIN>`
	if err := os.WriteFile(filepath.Join(base, "community.applications.plg"), []byte(caPLG), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2. DTD entity-based name attribute (double quotes) + config directory
	vaultPLG := `<?xml version="1.0" standalone="yes"?>
<!DOCTYPE PLUGIN [
    <!ENTITY name "Vault Backup Manager">
]>
<PLUGIN name="&name;" author="Ruaan Deysel">
</PLUGIN>`
	if err := os.WriteFile(filepath.Join(base, "vault.plg"), []byte(vaultPLG), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "vault"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 3. DTD entity-based name attribute (single quotes)
	udPLG := `<?xml version="1.0" standalone="yes"?>
<!DOCTYPE PLUGIN [
    <!ENTITY name 'Unassigned Devices'>
]>
<PLUGIN name="&name;">
</PLUGIN>`
	if err := os.WriteFile(filepath.Join(base, "unassigned.devices.plg"), []byte(udPLG), 0o644); err != nil {
		t.Fatal(err)
	}

	// 4. AppleDouble ._* artifact file (must be excluded)
	if err := os.WriteFile(filepath.Join(base, "._vault.plg"), []byte{0x00, 0x05, 0x16, 0x07}, 0o644); err != nil {
		t.Fatal(err)
	}

	// 5. Hidden dot file (must be excluded)
	if err := os.WriteFile(filepath.Join(base, ".hidden.plg"), []byte("<PLUGIN name=\"Hidden\"></PLUGIN>"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 6. Malformed XML (falls back to filename stem)
	if err := os.WriteFile(filepath.Join(base, "broken.plg"), []byte("this is not xml <><"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 7. Missing name attribute (falls back to filename stem)
	if err := os.WriteFile(filepath.Join(base, "noname.plg"), []byte("<PLUGIN author=\"Unknown\"></PLUGIN>"), 0o644); err != nil {
		t.Fatal(err)
	}

	h, err := NewPluginHandler()
	if err != nil {
		t.Fatalf("NewPluginHandler: %v", err)
	}

	items, err := h.ListItems()
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}

	itemsByName := make(map[string]BackupItem)
	for _, item := range items {
		itemsByName[item.Name] = item
	}

	// Assert ._ and hidden entries are excluded
	if _, found := itemsByName["._vault"]; found {
		t.Errorf("expected ._vault.plg to be excluded from ListItems()")
	}
	if _, found := itemsByName[".hidden"]; found {
		t.Errorf("expected .hidden.plg to be excluded from ListItems()")
	}

	// Assert valid fixtures
	wantItems := map[string]struct {
		displayName string
		hasConfig   bool
	}{
		"community.applications": {displayName: "Community Applications", hasConfig: false},
		"vault":                  {displayName: "Vault Backup Manager", hasConfig: true},
		"unassigned.devices":     {displayName: "Unassigned Devices", hasConfig: false},
		"broken":                 {displayName: "broken", hasConfig: false},
		"noname":                 {displayName: "noname", hasConfig: false},
	}

	if len(items) != len(wantItems) {
		t.Errorf("got %d items, want %d", len(items), len(wantItems))
	}

	for name, want := range wantItems {
		item, ok := itemsByName[name]
		if !ok {
			t.Errorf("missing expected plugin %q in results", name)
			continue
		}
		if item.Type != "plugin" {
			t.Errorf("item %q Type = %q, want plugin", name, item.Type)
		}
		disp, _ := item.Settings["display_name"].(string)
		if disp != want.displayName {
			t.Errorf("item %q display_name = %q, want %q", name, disp, want.displayName)
		}
		id, _ := item.Settings["id"].(string)
		if id != name {
			t.Errorf("item %q id = %q, want %q", name, id, name)
		}
		hasConfig, _ := item.Settings["has_config"].(bool)
		if hasConfig != want.hasConfig {
			t.Errorf("item %q has_config = %v, want %v", name, hasConfig, want.hasConfig)
		}
	}
}

func TestParsePluginDisplayName(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "literal name",
			input:    `<PLUGIN name="My Plugin"></PLUGIN>`,
			expected: "My Plugin",
		},
		{
			name: "entity double quotes",
			input: `<?xml version="1.0"?>
<!DOCTYPE PLUGIN [
<!ENTITY name "Dyn Name">
]>
<PLUGIN name="&name;">
</PLUGIN>`,
			expected: "Dyn Name",
		},
		{
			name: "entity single quotes",
			input: `<!DOCTYPE PLUGIN [
<!ENTITY name 'Single Quote'>
]>
<PLUGIN name="&name;">`,
			expected: "Single Quote",
		},
		{
			name:     "lowercase plugin tag",
			input:    `<plugin name="Lower Tag">`,
			expected: "Lower Tag",
		},
		{
			name:     "empty attribute",
			input:    `<PLUGIN name="">`,
			expected: "",
		},
		{
			name:     "missing name attribute",
			input:    `<PLUGIN author="Someone">`,
			expected: "",
		},
		{
			name:     "empty input",
			input:    "",
			expected: "",
		},
		{
			name:     "non-plugin root element",
			input:    `<OTHER name="Not a plugin">`,
			expected: "",
		},
		{
			name:     "invalid XML",
			input:    `random garbage bytes not xml`,
			expected: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePluginDisplayName([]byte(tc.input))
			if got != tc.expected {
				t.Errorf("parsePluginDisplayName() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestPluginDisplayName(t *testing.T) {
	// Missing file should gracefully return empty string
	if got := pluginDisplayName("/nonexistent/path/plugin.plg"); got != "" {
		t.Errorf("pluginDisplayName() = %q, want empty string", got)
	}

	// Valid file
	dir := t.TempDir()
	p := filepath.Join(dir, "test.plg")
	if err := os.WriteFile(p, []byte(`<PLUGIN name="Direct Test"></PLUGIN>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pluginDisplayName(p); got != "Direct Test" {
		t.Errorf("pluginDisplayName() = %q, want %q", got, "Direct Test")
	}
}

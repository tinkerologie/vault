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

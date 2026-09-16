//go:build linux

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// newPluginFixture points pluginsDir at a temp directory holding a .plg
// installer and a config directory for the named plugin, and returns the
// config directory.
func newPluginFixture(t *testing.T, name string) string {
	t.Helper()
	base := t.TempDir()
	orig := pluginsDir
	pluginsDir = base
	t.Cleanup(func() { pluginsDir = orig })

	if err := os.WriteFile(filepath.Join(base, name+".plg"), []byte("<PLUGIN/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(base, name)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return configDir
}

// writePluginFile writes a config file with an explicit mtime.
func writePluginFile(t *testing.T, dir, rel, content string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// TestPluginBackupHonoursChangedSince is the regression test for issue #351.
// The classic plugin path archived the whole config directory on every run and
// never read changed_since, so an incremental or differential plugin job was
// indistinguishable from a full one.
func TestPluginBackupHonoursChangedSince(t *testing.T) {
	changedSince := time.Now().Add(-1 * time.Hour)
	old := time.Now().Add(-3 * time.Hour)
	recent := time.Now().Add(-10 * time.Minute)

	cases := []struct {
		name string
		// settings beyond the plugin id, applied to the differential run.
		changedSince string
		prevListing  []string
		exclusions   []string
		wantPresent  []string
		wantAbsent   []string
	}{
		{
			name:         "only files changed since the cut-off are archived",
			changedSince: changedSince.UTC().Format(time.RFC3339),
			prevListing:  []string{"old.conf", "fresh.conf"},
			wantPresent:  []string{"fresh.conf"},
			wantAbsent:   []string{"old.conf"},
		},
		{
			// Issue #320's data-loss class, now reachable through plugins:
			// a file copied in with cp -a carries a stale mtime and is
			// invisible to an mtime test. Its absence from the parent
			// listing is what catches it.
			name:         "a new file with a stale mtime is caught by the parent listing",
			changedSince: changedSince.UTC().Format(time.RFC3339),
			prevListing:  []string{"fresh.conf"},
			wantPresent:  []string{"fresh.conf", "old.conf"},
		},
		{
			name:        "no cut-off archives everything",
			wantPresent: []string{"old.conf", "fresh.conf"},
		},
		{
			// The classic path passed nil exclusions, so a user's exclude
			// list was silently ignored for plugin items.
			name:        "exclusions are honoured",
			exclusions:  []string{"old.conf"},
			wantPresent: []string{"fresh.conf"},
			wantAbsent:  []string{"old.conf"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configDir := newPluginFixture(t, "myplugin")
			writePluginFile(t, configDir, "old.conf", "old", old)
			writePluginFile(t, configDir, "fresh.conf", "fresh", recent)

			settings := map[string]any{"id": "myplugin"}
			if tc.changedSince != "" {
				settings["changed_since"] = tc.changedSince
			}
			if tc.prevListing != nil {
				settings["prev_listing_paths"] = tc.prevListing
			}
			if tc.exclusions != nil {
				settings["exclude_paths"] = tc.exclusions
			}

			destDir := t.TempDir()
			result, err := (&PluginHandler{}).Backup(context.Background(),
				BackupItem{Name: "myplugin", Type: "plugin", Settings: settings, Compression: CompressionNone},
				destDir, noopProgress)
			if err != nil {
				t.Fatalf("Backup: %v", err)
			}
			if !result.Success {
				t.Fatal("expected result.Success")
			}

			archive := filepath.Join(destDir, "config.tar")
			names := listTarEntries(t, archive)
			for _, want := range tc.wantPresent {
				if !containsName(names, want) {
					t.Errorf("archive missing %s: %v", want, names)
				}
			}
			for _, notWant := range tc.wantAbsent {
				if containsName(names, notWant) {
					t.Errorf("archive should not contain %s: %v", notWant, names)
				}
			}
		})
	}
}

// TestPluginBackupWritesEffectiveListing pins the prerequisite for the whole
// flow: without this sidecar the runner has no parent listing to load, so
// every plugin differential degrades to a full archive and the next one
// cannot improve on it.
func TestPluginBackupWritesEffectiveListing(t *testing.T) {
	configDir := newPluginFixture(t, "myplugin")
	writePluginFile(t, configDir, "a.conf", "a", time.Now())
	writePluginFile(t, configDir, "sub/b.conf", "b", time.Now())

	destDir := t.TempDir()
	result, err := (&PluginHandler{}).Backup(context.Background(),
		BackupItem{Name: "myplugin", Type: "plugin",
			Settings:    map[string]any{"id": "myplugin", "exclude_paths": []string{"sub"}},
			Compression: CompressionNone},
		destDir, noopProgress)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}

	listingPath := filepath.Join(destDir, "config.tar"+ListingSuffix)
	f, err := os.Open(listingPath)
	if err != nil {
		t.Fatalf("effective listing sidecar missing: %v", err)
	}
	defer f.Close()
	idx, err := ReadTarIndex(f)
	if err != nil {
		t.Fatalf("reading listing: %v", err)
	}

	got := map[string]bool{}
	for _, entry := range idx.Files {
		got[entry.Path] = true
	}
	if !got["a.conf"] {
		t.Errorf("listing missing a.conf: %+v", idx.Files)
	}
	if got["sub/b.conf"] {
		t.Errorf("listing must record the EFFECTIVE set, after exclusions: %+v", idx.Files)
	}

	// The sidecar has to be registered as a result file or it is never
	// uploaded, and the next run finds nothing to load.
	var registered bool
	for _, fi := range result.Files {
		if fi.Name == filepath.Base(listingPath) {
			registered = true
		}
	}
	if !registered {
		t.Errorf("listing sidecar was written but not registered in result.Files: %+v", result.Files)
	}
}

// TestPluginBackupChunkedForwardsChangedSince pins the dedup half of issue
// #351: PluginHandler.BackupChunked dropped changed_since before proxying to
// the folder handler, so buildChunkedManifest's carry-forward branch never ran
// and every file was re-chunked on every run.
func TestPluginBackupChunkedForwardsChangedSince(t *testing.T) {
	base := t.TempDir()
	orig := pluginsDir
	pluginsDir = base
	t.Cleanup(func() { pluginsDir = orig })

	src := t.TempDir()
	old := time.Now().Add(-3 * time.Hour)
	writePluginFile(t, src, "old.conf", "old", old)

	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	h := &PluginHandler{}
	ctx := context.Background()

	fullID, err := h.BackupChunked(ctx,
		BackupItem{Name: "p", Type: "plugin", Settings: map[string]any{"path": src}}, repo, nil, nil)
	if err != nil {
		t.Fatalf("full BackupChunked: %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	parent, err := repo.GetManifest(fullID)
	if err != nil {
		t.Fatal(err)
	}

	// A file changed after the cut-off, alongside one that did not.
	writePluginFile(t, src, "fresh.conf", "fresh", time.Now())

	var chunked []string
	progress := func(_ string, _ int, msg string) {
		if strings.HasPrefix(msg, "chunked ") {
			chunked = append(chunked, strings.TrimPrefix(msg, "chunked "))
		}
	}

	diffID, err := h.BackupChunked(ctx,
		BackupItem{Name: "p", Type: "plugin", Settings: map[string]any{
			"path":          src,
			"changed_since": time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
		}}, repo, &parent, progress)
	if err != nil {
		t.Fatalf("differential BackupChunked: %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	diff, err := repo.GetManifest(diffID)
	if err != nil {
		t.Fatal(err)
	}

	// The manifest must stay complete — a differential that dropped the
	// unchanged file could not restore from a single point.
	if _, ok := diff.Files["old.conf"]; !ok {
		t.Errorf("unchanged file was not carried forward: %+v", diff.Files)
	}
	if _, ok := diff.Files["fresh.conf"]; !ok {
		t.Errorf("changed file missing from the differential: %+v", diff.Files)
	}

	hasFresh := false
	hasOld := false
	for _, c := range chunked {
		if strings.HasSuffix(c, "fresh.conf") {
			hasFresh = true
		}
		if strings.HasSuffix(c, "old.conf") {
			hasOld = true
		}
	}
	if !hasFresh {
		t.Errorf("fresh.conf was not chunked: %v", chunked)
	}
	if hasOld {
		t.Errorf("old.conf was re-chunked instead of carried forward: %v", chunked)
	}
}

// TestPluginBackupChunkedForwardsExclusions pins that a plugin job's exclude
// list reaches the folder proxy — it was dropped along with changed_since.
func TestPluginBackupChunkedForwardsExclusions(t *testing.T) {
	base := t.TempDir()
	orig := pluginsDir
	pluginsDir = base
	t.Cleanup(func() { pluginsDir = orig })

	src := t.TempDir()
	writePluginFile(t, src, "keep.conf", "keep", time.Now())
	writePluginFile(t, src, "skip.conf", "skip", time.Now())

	repo, _, cleanup := dedup.NewTestRepoForEngine(t)
	defer cleanup()
	ctx := context.Background()

	id, err := (&PluginHandler{}).BackupChunked(ctx,
		BackupItem{Name: "p", Type: "plugin", Settings: map[string]any{
			"path":          src,
			"exclude_paths": []string{"skip.conf"},
		}}, repo, nil, nil)
	if err != nil {
		t.Fatalf("BackupChunked: %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	m, err := repo.GetManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Files["keep.conf"]; !ok {
		t.Errorf("keep.conf missing from the manifest: %+v", m.Files)
	}
	if _, ok := m.Files["skip.conf"]; ok {
		t.Errorf("skip.conf should have been excluded: %+v", m.Files)
	}
}

// TestPluginBackupArchiveFailureIsReported pins that a failed config archive
// aborts the backup. Both the incremental and the full branch have to do it:
// returning the result anyway would register a plugin backup whose config
// archive is missing or truncated, and the run would be reported as a success.
func TestPluginBackupArchiveFailureIsReported(t *testing.T) {
	cases := []struct {
		name         string
		changedSince string
		wantMessage  string
	}{
		{
			name:        "full archive",
			wantMessage: "archiving plugin config",
		},
		{
			name:         "incremental archive",
			changedSince: time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
			wantMessage:  "archiving changed plugin config files",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configDir := newPluginFixture(t, "myplugin")
			writePluginFile(t, configDir, "a.conf", "a", time.Now())

			// A directory where the archive belongs: creating the archive
			// file cannot succeed, whatever the process runs as.
			destDir := t.TempDir()
			if err := os.Mkdir(filepath.Join(destDir, "config.tar"), 0o750); err != nil {
				t.Fatal(err)
			}

			settings := map[string]any{"id": "myplugin"}
			if tc.changedSince != "" {
				settings["changed_since"] = tc.changedSince
			}

			_, err := (&PluginHandler{}).Backup(context.Background(),
				BackupItem{Name: "myplugin", Type: "plugin", Settings: settings, Compression: CompressionNone},
				destDir, noopProgress)
			if err == nil {
				t.Fatal("a config archive that cannot be written must fail the backup")
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Errorf("error %q should name the failed step (%q)", err, tc.wantMessage)
			}
		})
	}
}

// TestPluginBackupMalformedChangedSinceArchivesEverything pins the safe
// fallback: an unparseable cut-off must not silently filter the archive down
// to nothing — the whole config directory is captured instead.
func TestPluginBackupMalformedChangedSinceArchivesEverything(t *testing.T) {
	configDir := newPluginFixture(t, "myplugin")
	writePluginFile(t, configDir, "old.conf", "old", time.Now().Add(-3*time.Hour))
	writePluginFile(t, configDir, "fresh.conf", "fresh", time.Now())

	destDir := t.TempDir()
	if _, err := (&PluginHandler{}).Backup(context.Background(),
		BackupItem{Name: "myplugin", Type: "plugin", Compression: CompressionNone,
			Settings: map[string]any{"id": "myplugin", "changed_since": "not-a-timestamp"}},
		destDir, noopProgress); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	names := listTarEntries(t, filepath.Join(destDir, "config.tar"))
	for _, want := range []string{"old.conf", "fresh.conf"} {
		if !containsName(names, want) {
			t.Errorf("archive missing %s — an unparseable cut-off must fall back to a full archive: %v", want, names)
		}
	}
}

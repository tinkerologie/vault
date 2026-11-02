package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Regression checks for removing the legacy PHP UI (#533). Tests run with
// scripts/ as the working directory, so the repo root is "..".

const (
	cleanupBegin = "# BEGIN legacy-ui-cleanup"
	cleanupEnd   = "# END legacy-ui-cleanup"
	pluginDir    = "/usr/local/emhttp/plugins/vault"
)

var legacyPages = []string{
	"VaultJobs.page", "VaultRestore.page", "VaultStorage.page",
	"VaultHistory.page", "VaultSettings.page",
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// cleanupBlock returns the marked block from the manifest, failing unless each
// marker occurs exactly once and in order.
func cleanupBlock(t *testing.T, plg string) string {
	t.Helper()
	for _, marker := range []string{cleanupBegin, cleanupEnd} {
		if n := strings.Count(plg, marker); n != 1 {
			t.Fatalf("marker %q occurs %d times, want 1", marker, n)
		}
	}
	begin, end := strings.Index(plg, cleanupBegin), strings.Index(plg, cleanupEnd)
	if begin > end {
		t.Fatal("cleanup markers are out of order")
	}
	return plg[begin : end+len(cleanupEnd)]
}

// The package recipe globs plugin/pages/*.page, so this inventory is exactly
// what ships. A new page must be added here on purpose.
func TestPackageShipsOnlyRetainedPages(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "plugin", "pages", "*.page"))
	if err != nil {
		t.Fatal(err)
	}
	pages := make([]string, 0, len(matches))
	for _, match := range matches {
		pages = append(pages, filepath.Base(match))
	}
	slices.Sort(pages)
	if want := []string{"Backups.page", "Vault.page"}; !slices.Equal(pages, want) {
		t.Errorf("plugin/pages/*.page = %v, want %v", pages, want)
	}

	if _, err := os.Stat(filepath.Join("..", "plugin", "assets")); !os.IsNotExist(err) {
		t.Errorf("plugin/assets must not exist: stat err = %v", err)
	}
	for _, include := range []string{"api.php", "app.php", "apply.sh", "control.php", "proxy.php"} {
		if _, err := os.Stat(filepath.Join("..", "plugin", "pages", "include", include)); err != nil {
			t.Errorf("retained include: %v", err)
		}
	}
}

func TestPackageRecipeDoesNotStageAssets(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	recipe := regexp.MustCompile(`(?m)^package:.*\n((?:\t.*\n)+)`).FindStringSubmatch(makefile)
	if recipe == nil {
		t.Fatal("package target not found in Makefile")
	}
	if strings.Contains(recipe[1], "plugin/assets") {
		t.Error("package target still copies plugin/assets")
	}
	for _, want := range []string{"plugin/pages/*.page", "plugin/pages/include", "web/dist/."} {
		if !strings.Contains(recipe[1], want) {
			t.Errorf("package target no longer copies %s", want)
		}
	}
}

// Upgrades must delete leftovers from the installed tree: run the real manifest
// block twice against a seeded copy and require everything else to be untouched.
func TestManifestCleanupRemovesOnlyLegacyFiles(t *testing.T) {
	block := cleanupBlock(t, readRepoFile(t, "plugin/vault.plg"))
	if n := strings.Count(block, `"`+pluginDir+`"`); n != 1 {
		t.Fatalf("plugin dir assignment occurs %d times, want 1", n)
	}

	root := t.TempDir()
	block = strings.Replace(block, `"`+pluginDir+`"`, `"`+root+`"`, 1)

	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	legacy := []string{"assets/css/vault.css", "assets/js/vault.js", "assets/js/jobs.js", "assets/js/restore.js"}
	legacy = append(legacy, legacyPages...)
	for _, rel := range legacy {
		write(rel, "legacy")
	}
	retained := []string{
		"Vault.page", "Backups.page", "README.md", "vault.cfg",
		"include/api.php", "include/app.php", "include/apply.sh", "include/control.php", "include/proxy.php",
		"event/started", "event/stopping_svcs",
		"ui/index.html", "ui/assets/index-abc123.js",
	}
	for _, rel := range retained {
		write(rel, "keep:"+rel)
	}

	// The second run proves the block still exits 0 once the files are gone.
	for run := 1; run <= 2; run++ {
		cmd := exec.Command("sh", "-c", "set -e\n"+block)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			t.Fatalf("run %d: %v: %s", run, err, output.String())
		}
		for _, rel := range append([]string{"assets"}, legacyPages...) {
			if _, err := os.Lstat(filepath.Join(root, rel)); !os.IsNotExist(err) {
				t.Errorf("run %d: %s still present: %v", run, rel, err)
			}
		}
		for _, rel := range retained {
			got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				t.Errorf("run %d: retained file: %v", run, err)
			} else if string(got) != "keep:"+rel {
				t.Errorf("run %d: %s changed: %q", run, rel, got)
			}
		}
	}
}

func TestManifestCleanupNoopWhenPluginDirMissing(t *testing.T) {
	block := cleanupBlock(t, readRepoFile(t, "plugin/vault.plg"))
	missing := filepath.Join(t.TempDir(), "missing")
	block = strings.Replace(block, `"`+pluginDir+`"`, `"`+missing+`"`, 1)
	if out, err := exec.Command("sh", "-c", "set -e\n"+block).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Errorf("cleanup created the missing plugin dir: %v", err)
	}
}

// The cleanup must run on every install after the package lands and before the
// daemon starts, and must not be able to delete the freshly installed SPA.
func TestManifestCleanupPlacement(t *testing.T) {
	plg := readRepoFile(t, "plugin/vault.plg")
	block := cleanupBlock(t, plg)

	install := strings.Index(plg, `Run="upgradepkg --install-new"`)
	begin := strings.Index(plg, cleanupBegin)
	end := strings.Index(plg, cleanupEnd)
	start := strings.Index(plg, "/etc/rc.d/rc.vault start")
	remove := strings.Index(plg, `Method="remove"`)
	if install < 0 || start < 0 || remove < 0 {
		t.Fatalf("manifest anchors missing: install=%d start=%d remove=%d", install, start, remove)
	}
	if install >= begin || end >= start || end >= remove {
		t.Errorf("cleanup out of place: install=%d begin=%d end=%d start=%d remove=%d", install, begin, end, start, remove)
	}

	if regexp.MustCompile(`/ui\b`).MatchString(block) {
		t.Error("cleanup block must not reference ui/")
	}
	if strings.ContainsAny(block, "*?") {
		t.Error("cleanup block must use exact paths, not globs")
	}
	for _, page := range legacyPages {
		if !strings.Contains(block, page) {
			t.Errorf("cleanup block does not remove %s", page)
		}
	}
}

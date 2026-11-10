package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Exercise the actual publication script in an isolated repository. A public
// commit must contain the matching version, checksum and notes together.
func TestReleaseManifestPublication(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/release.yml")
	_, script, ok := strings.Cut(workflow, "cat > \"$RUNNER_TEMP/update-plg.sh\" <<'SCRIPT'\n")
	if !ok {
		t.Fatal("publication script missing")
	}
	script, _, ok = strings.Cut(script, "          SCRIPT\n")
	if !ok {
		t.Fatal("publication script terminator missing")
	}
	script = strings.ReplaceAll(script, "\n          ", "\n")
	script = strings.TrimPrefix(script, "          ")
	runtimeDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(runtimeDir, "plgnotes"), "./cmd/plgnotes")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v: %s", err, out)
	}
	for _, tc := range []struct {
		name, version, published, target, notes string
		wantFailure                             bool
	}{
		{"publish", "2026.11.01", "2026.11.00", "2026.11.01", "### Fixed\n\n- Safe publication.\n", false},
		{"newer candidate", "2026.11.02", "2026.11.00", "2026.11.01", "- Safe publication.\n", false},
		{"newer published", "2026.11.02", "2026.11.02", "2026.11.01", "- Safe publication.\n", true},
		{"numeric ordering", "2026.11.10", "2026.11.9", "2026.11.10", "- Safe publication.\n", false},
		{"stable replaces rc", "2026.11.01", "2026.11.01-rc1", "2026.11.01", "- Safe publication.\n", false},
		{"rc cannot replace stable", "2026.11.01", "2026.11.01", "2026.11.01-rc2", "- Safe publication.\n", true},
		{"invalid published", "2026.11.01", "invalid", "2026.11.01", "- Safe publication.\n", true},
		{"invalid target", "2026.11.01", "2026.11.00", "invalid", "- Safe publication.\n", true},
		{"invalid notes", "2026.11.01", "2026.11.00", "2026.11.01", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(path, content string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			manifest := readRepoFile(t, "plugin/vault.plg")
			manifest = regexp.MustCompile(`(?m)^(\s*<!ENTITY version\s+")[^"]+(">)$`).ReplaceAllString(manifest, "${1}"+tc.published+"${2}")
			write(filepath.Join(dir, "plugin/vault.plg"), manifest)
			write(filepath.Join(dir, "VERSION"), tc.version+"\n")
			write(filepath.Join(runtimeDir, "release-notes/RELEASE_NOTES_BASE.md"), tc.notes)
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return string(out)
			}
			git("init", "-q")
			git("config", "user.name", "Release test")
			git("config", "user.email", "release@example.invalid")
			git("config", "commit.gpgsign", "false")
			git("config", "core.hooksPath", filepath.Join(dir, "no-hooks"))
			git("add", ".")
			git("commit", "-qm", "Initial published release")
			before := git("rev-parse", "HEAD")
			run := func() error {
				cmd := exec.Command("bash", "-c", script)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "RUNNER_TEMP="+runtimeDir, "VERSION_VERSION="+tc.target, "CHECKSUM_SHA256="+strings.Repeat("a", 64))
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Logf("publication: %s", out)
				}
				return err
			}
			if err := run(); (err != nil) != tc.wantFailure {
				t.Fatalf("publication error = %v, want failure %v", err, tc.wantFailure)
			}
			if tc.wantFailure {
				if got := git("rev-parse", "HEAD"); got != before {
					t.Fatal("failed publication changed the public commit")
				}
				return
			}
			published := git("show", "HEAD:plugin/vault.plg")
			for _, want := range []string{`<!ENTITY version   "` + tc.target + `">`, `<!ENTITY sha256      "` + strings.Repeat("a", 64) + `">`, "## v" + tc.target, "- Safe publication."} {
				if !strings.Contains(published, want) {
					t.Errorf("published manifest missing %q", want)
				}
			}
			after := git("rev-parse", "HEAD")
			if err := run(); err != nil || git("rev-parse", "HEAD") != after {
				t.Fatal("publication retry must be idempotent")
			}
		})
	}
}

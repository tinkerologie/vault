//go:build !windows

package safepath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateLocalStorageRoot514(t *testing.T) {
	t.Parallel()
	rejected := map[string]string{
		"":                          "required",
		"   ":                       "required",
		".":                         "absolute",
		"relative/dir":              "absolute",
		"./backups":                 "absolute",
		"/":                         "protected",
		"//":                        "protected",
		"/usr":                      "protected",
		"/usr/":                     "protected",
		"/usr/local":                "protected",
		"/usr/local/emhttp":         "protected",
		"/usr/local/emhttp/plugins": "protected",
		"/etc":                      "protected",
		"/boot":                     "protected",
		"/boot/config":              "protected",
		"/var":                      "protected", // ancestor of /var/lib
		"/var/lib/docker":           "protected",
		"/var/local/emhttp":         "protected",
		"/root":                     "protected",
		"/proc/self":                "protected",
		"/mnt/user/../../usr":       "protected",
		"/mnt":                      "shared parent",
		"/mnt/":                     "shared parent",
		"/mnt/user":                 "shared parent",
		"/mnt/user/":                "shared parent",
		"/mnt/user0":                "shared parent",
		"/mnt/disks":                "shared parent",
		"/mnt/remotes":              "shared parent",
		"/mnt/user/backups/..":      "shared parent",
		"/tmp":                      "shared parent",
	}
	for in, want := range rejected {
		if _, err := ValidateLocalStorageRoot(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateLocalStorageRoot(%q) = %v, want error containing %q", in, err, want)
		}
	}

	accepted := map[string]string{
		"/mnt/user/Documents/appdata-backup": "/mnt/user/Documents/appdata-backup",
		"/mnt/user/backups/vault/":           "/mnt/user/backups/vault",
		"  /mnt/user/backups  ":              "/mnt/user/backups",
		"/mnt/cache/vault":                   "/mnt/cache/vault",
		"/mnt/backup_pool":                   "/mnt/backup_pool", // a dedicated pool root
		"/mnt/disks/usb1/vault":              "/mnt/disks/usb1/vault",
		"/mnt/remotes/nas_backups/vault":     "/mnt/remotes/nas_backups/vault",
		"/tmp/vault-nfs-123":                 "/tmp/vault-nfs-123",
		"/mnt/user/backups/./x/../vault":     "/mnt/user/backups/vault",
	}
	for in, want := range accepted {
		got, err := ValidateLocalStorageRoot(in)
		if err != nil || got != want {
			t.Errorf("ValidateLocalStorageRoot(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
}

// A symlink inside an ordinary directory must not smuggle a protected
// target in, and a symlinked base pointing at a safe directory still works.
// Validation only: nothing is written through the links.
func TestValidateLocalStorageRootSymlinks514(t *testing.T) {
	t.Parallel()
	base := t.TempDir()

	toUsr := filepath.Join(base, "to-usr")
	if err := os.Symlink("/usr", toUsr); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ValidateLocalStorageRoot(toUsr); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Errorf("symlink to /usr: got %v, want protected", err)
	}
	// A not-yet-created child under the link resolves through it too.
	if _, err := ValidateLocalStorageRoot(filepath.Join(toUsr, "local", "emhttp", "new")); err == nil {
		t.Error("missing child below a symlink to /usr was accepted")
	}

	toRoot := filepath.Join(base, "to-root")
	if err := os.Symlink("/", toRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateLocalStorageRoot(toRoot); err == nil {
		t.Error("symlink to / was accepted")
	}

	safe := filepath.Join(base, "real-backups")
	if err := os.Mkdir(safe, 0o755); err != nil {
		t.Fatal(err)
	}
	toSafe := filepath.Join(base, "link-backups")
	if err := os.Symlink(safe, toSafe); err != nil {
		t.Fatal(err)
	}
	if got, err := ValidateLocalStorageRoot(toSafe); err != nil || got != toSafe {
		t.Errorf("symlink to a safe dir: got %q, %v", got, err)
	}
	if got, err := ValidateLocalStorageRoot(filepath.Join(toSafe, "not-yet")); err != nil || got != filepath.Join(toSafe, "not-yet") {
		t.Errorf("missing child of a safe link: got %q, %v", got, err)
	}

	// The test's own temp dir (macOS: /private/var/folders/...) is valid.
	if _, err := ValidateLocalStorageRoot(base); err != nil {
		t.Errorf("t.TempDir() root rejected: %v", err)
	}
}

func TestValidateLocalStorageRootLoopFailsClosed514(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateLocalStorageRoot(a); err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
		t.Errorf("symlink loop: got %v, want resolution failure", err)
	}
}

func TestNormalizeRestorePointPath514(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", " ", ".", "./", "/", "..", "../x", "job/../..", "/abs/run", "job", "job/", "./job"} {
		if got, err := NormalizeRestorePointPath(in); err == nil {
			t.Errorf("NormalizeRestorePointPath(%q) = %q, want error", in, got)
		}
	}
	for in, want := range map[string]string{
		"job/run":                     "job/run",
		"job/run/":                    "job/run",
		"./job/run":                   "job/run",
		"Daily/Docker/2026-01-01_000": "Daily/Docker/2026-01-01_000", // job names may contain '/'
		"job/1_2026-01-15_020000":     "job/1_2026-01-15_020000",
	} {
		if got, err := NormalizeRestorePointPath(in); err != nil || got != want {
			t.Errorf("NormalizeRestorePointPath(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
}

func TestIsStrictDescendant514(t *testing.T) {
	t.Parallel()
	cases := []struct {
		parent, child string
		want          bool
	}{
		{"job/run", "job/run/f.bin", true},
		{"job/run", "job/run/a/b", true},
		{"job/run/", "job/run/f.bin", true},
		{"job/run", "job/run", false},
		{"job/run", "job/run-evil/f", false},
		{"job/run", "job/other", false},
		{"job/run", "job/run/../x", false},
		{"job/run", "/job/run/f", false},
		{"job/run", "..", false},
		{"job/run", ".", false},
		{".", "anything", true},
		{".", "../x", false},
	}
	for _, tc := range cases {
		if got := IsStrictDescendant(tc.parent, tc.child); got != tc.want {
			t.Errorf("IsStrictDescendant(%q, %q) = %v, want %v", tc.parent, tc.child, got, tc.want)
		}
	}
}

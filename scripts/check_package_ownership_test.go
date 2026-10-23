package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildPackage writes a one-file .txz whose entries are owned by uid:gid.
func buildPackage(t *testing.T, uid string) string {
	t.Helper()
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "usr", "local", "emhttp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "usr", "local", "emhttp", "page"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(t.TempDir(), "vault.txz")
	cmd := exec.Command("tar", "--owner="+uid, "--group="+uid, "--numeric-owner", "-cJf", pkg, "usr/")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("tar cannot build a fixture here: %v: %s", err, out)
	}
	return pkg
}

func runCheck(pkg string, env ...string) (string, error) {
	args := []string{"check-package-ownership.sh"}
	if pkg != "" {
		args = append(args, pkg)
	}
	cmd := exec.Command("sh", args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fakeTar writes a tar stand-in that prints listing for any arguments, so
// both GNU tar ("0/0") and bsdtar ("0 0") formats are tested on any host.
func fakeTar(t *testing.T, listing string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tar")
	script := "#!/bin/sh\ncat <<'LISTING'\n" + listing + "\nLISTING\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckPackageOwnershipListingFormats(t *testing.T) {
	cases := map[string]struct {
		listing string
		ok      bool
	}{
		"gnu root":     {"drwxr-xr-x 0/0 0 2026-10-17 21:51 usr/\n-rw-r--r-- 0/0 5 2026-10-17 21:51 usr/x", true},
		"gnu runner":   {"drwxr-xr-x 0/0 0 2026-10-17 21:51 usr/\ndrwxr-xr-x 1001/1001 0 2026-10-17 21:51 usr/local/", false},
		"bsd root":     {"drwxr-xr-x  0 0      0           0 17 Oct 21:51 usr/\n-rw-r--r--  0 0      0           5 17 Oct 21:51 usr/x", true},
		"bsd runner":   {"drwxr-xr-x  0 0      0           0 17 Oct 21:51 usr/\ndrwxr-xr-x  0 1001   1001        0 17 Oct 21:51 usr/local/", false},
		"bsd gid only": {"drwxr-xr-x  0 0      100         0 17 Oct 21:51 usr/", false},
	}
	for name, tc := range cases {
		out, err := runCheck("pkg.txz", "TAR="+fakeTar(t, tc.listing))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v out=%s, want ok=%v", name, err, out, tc.ok)
		}
	}
}

func TestCheckPackageOwnershipUsage(t *testing.T) {
	if out, err := runCheck(""); err == nil || !strings.Contains(out, "usage") {
		t.Fatalf("no argument: err=%v out=%s, want usage error", err, out)
	}
}

// `make package` must build root-owned entries and run this check.
func TestPackageTargetUsesOwnershipCheck(t *testing.T) {
	mk, err := os.ReadFile(filepath.Join("..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	recipe := string(mk)
	start := strings.Index(recipe, "\npackage:")
	if start < 0 {
		t.Fatal("no package target in the Makefile")
	}
	end := strings.Index(recipe[start+1:], "\n\n")
	if end < 0 {
		end = len(recipe) - start - 1
	}
	recipe = recipe[start : start+1+end]
	for _, want := range []string{"tar --owner=0 --group=0 --numeric-owner", "sh scripts/check-package-ownership.sh"} {
		if !strings.Contains(recipe, want) {
			t.Errorf("package target is missing %q", want)
		}
	}
}

// The package ownership check behind `make package` (#514).
func TestCheckPackageOwnership(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	if out, err := runCheck(buildPackage(t, "0")); err != nil {
		t.Fatalf("root-owned package rejected: %v: %s", err, out)
	}
	out, err := runCheck(buildPackage(t, "1001"))
	if err == nil || !strings.Contains(out, "not owned by root:root") {
		t.Fatalf("UID 1001 package accepted: %v: %s", err, out)
	}
	out, err = runCheck(filepath.Join(t.TempDir(), "missing.txz"))
	if err == nil || !strings.Contains(out, "cannot list") {
		t.Fatalf("unreadable package accepted: %v: %s", err, out)
	}
}

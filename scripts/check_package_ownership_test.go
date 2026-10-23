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

// `make package` builds the archive through make-package-archive.sh.
func TestPackageTargetUsesArchiveScript(t *testing.T) {
	mk, err := os.ReadFile(filepath.Join("..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mk), "sh scripts/make-package-archive.sh $(BUILD_DIR)/pkg $(BUILD_DIR)/$(BINARY)-$(VERSION).txz") {
		t.Fatal("the package target does not build through scripts/make-package-archive.sh")
	}
}

// The archive script stores root:root entries even when the staged files
// belong to someone else, runs the ownership check, and fails if tar fails.
func TestMakePackageArchive(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	src := t.TempDir()
	for _, d := range []string{"usr/local/sbin", "etc/rc.d"} {
		if err := os.MkdirAll(filepath.Join(src, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "usr/local/sbin/vault"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "vault.txz")
	if b, err := exec.Command("sh", "make-package-archive.sh", src, out).CombinedOutput(); err != nil {
		t.Fatalf("make-package-archive.sh: %v: %s", err, b)
	}
	listing, err := exec.Command("tar", "--numeric-owner", "-tvJf", out).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(listing), "usr/local/sbin/vault") || !strings.Contains(string(listing), "etc/rc.d") {
		t.Fatalf("archive is missing staged entries:\n%s", listing)
	}
	if b, err := exec.Command("sh", "check-package-ownership.sh", out).CombinedOutput(); err != nil {
		t.Fatalf("built archive fails the ownership check: %v: %s", err, b)
	}

	cmd := exec.Command("sh", "make-package-archive.sh", src, filepath.Join(t.TempDir(), "x.txz"))
	cmd.Env = append(os.Environ(), "TAR=false")
	if b, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("a failing tar was accepted: %s", b)
	}
	if b, err := exec.Command("sh", "make-package-archive.sh", src).CombinedOutput(); err == nil || !strings.Contains(string(b), "usage") {
		t.Fatalf("missing argument: %v: %s", err, b)
	}
}

// A failing awk must not be read as "no bad entries".
func TestCheckPackageOwnershipAwkFailure(t *testing.T) {
	listing := "drwxr-xr-x 1001/1001 0 2026-10-17 21:51 usr/"
	out, err := runCheck("pkg.txz", "TAR="+fakeTar(t, listing), "AWK=false")
	if err == nil || !strings.Contains(out, "cannot check ownership") {
		t.Fatalf("awk failure accepted: err=%v out=%s", err, out)
	}
}

// A tar that cannot list the archive fails the check.
func TestCheckPackageOwnershipListFailure(t *testing.T) {
	out, err := runCheck("pkg.txz", "TAR=false")
	if err == nil || !strings.Contains(out, "cannot list") {
		t.Fatalf("listing failure accepted: err=%v out=%s", err, out)
	}
}

// The archive script fails when the archive it built is not root-owned.
func TestMakePackageArchiveOwnershipFailure(t *testing.T) {
	src := t.TempDir()
	for _, d := range []string{"usr", "etc"} {
		if err := os.MkdirAll(filepath.Join(src, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A tar stand-in that "creates" the archive and lists a UID 1001 entry.
	stub := filepath.Join(t.TempDir(), "tar")
	script := "#!/bin/sh\ncase \"$*\" in *-cJf*) for a; do last=$a; done; prev=; for a; do [ \"$prev\" = -cJf ] && : > \"$a\"; prev=$a; done ;; *) echo 'drwxr-xr-x 1001/1001 0 2026-10-17 21:51 usr/' ;; esac\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "make-package-archive.sh", src, filepath.Join(t.TempDir(), "x.txz"))
	cmd.Env = append(os.Environ(), "TAR="+stub)
	b, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(b), "not owned by root:root") {
		t.Fatalf("a non-root archive was accepted: %v: %s", err, b)
	}
}

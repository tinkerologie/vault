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

func runCheck(pkg string) (string, error) {
	out, err := exec.Command("sh", "check-package-ownership.sh", pkg).CombinedOutput()
	return string(out), err
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

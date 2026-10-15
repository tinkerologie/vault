package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// c458FakeQemuImg installs a shell-script stand-in for qemu-img at the front
// of PATH so flattenVMChain's rebase/convert steps run deterministically on
// hosts with or without the real binary. failOn names the subcommand
// ("rebase" or "convert") that should exit non-zero; empty means succeed.
// "convert -O qcow2 SRC DST" copies SRC to DST so the output is observable.
func c458FakeQemuImg(t *testing.T, failOn string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script qemu-img stub needs a POSIX shell")
	}
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> '" + logPath + "'\n" +
		"if [ \"$1\" = \"" + failOn + "\" ]; then echo \"boom $1\"; exit 3; fi\n" +
		"if [ \"$1\" = \"convert\" ]; then cp \"$4\" \"$5\"; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "qemu-img"), []byte(script), 0o755); err != nil { // #nosec G306 -- test stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

const c458TwoStepMetaFull = `{"disks":[{"target":"vda","backup_file":"vdisk0.img","format":"qcow2"}]}`
const c458TwoStepMetaInc = `{"disks":[{"target":"vda","backup_file":"vdisk0.qcow2","format":"qcow2"}]}`

func c458TwoStepChain(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	full := filepath.Join(root, "full")
	inc := filepath.Join(root, "inc")
	writeChainStep(t, full, c458TwoStepMetaFull, map[string]string{"vdisk0.img": "base-layer"})
	writeChainStep(t, inc, c458TwoStepMetaInc, map[string]string{
		"vdisk0.qcow2": "top-layer",
		"domain.xml":   "<domain/>",
	})
	if err := os.Mkdir(filepath.Join(inc, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	return full, inc
}

func TestFlattenVMChainQemuSuccessC458(t *testing.T) {
	logPath := c458FakeQemuImg(t, "")
	full, inc := c458TwoStepChain(t)
	final := filepath.Join(t.TempDir(), "final")

	if err := flattenVMChain(context.Background(), []string{full, inc}, final); err != nil {
		t.Fatalf("flattenVMChain: %v", err)
	}
	// Output disk uses the full step's filename and carries the top layer.
	got, err := os.ReadFile(filepath.Join(final, "vdisk0.img"))
	if err != nil || string(got) != "top-layer" {
		t.Fatalf("flattened disk = %q, %v", got, err)
	}
	// Sidecars copied; the top-step disk file is not copied verbatim.
	if b, err := os.ReadFile(filepath.Join(final, "domain.xml")); err != nil || string(b) != "<domain/>" {
		t.Fatalf("domain.xml = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(final, "vm_meta.json")); err != nil {
		t.Fatalf("vm_meta.json not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(final, "vdisk0.qcow2")); !os.IsNotExist(err) {
		t.Fatalf("top-step disk should not be copied as a sidecar: %v", err)
	}
	calls, _ := os.ReadFile(logPath)
	if !strings.Contains(string(calls), "rebase -u -F qcow2") || !strings.Contains(string(calls), "convert -O qcow2") {
		t.Fatalf("unexpected qemu-img calls: %s", calls)
	}
}

func TestFlattenVMChainRebaseFailsC458(t *testing.T) {
	c458FakeQemuImg(t, "rebase")
	full, inc := c458TwoStepChain(t)
	err := flattenVMChain(context.Background(), []string{full, inc}, filepath.Join(t.TempDir(), "f"))
	if err == nil || !strings.Contains(err.Error(), "qemu-img rebase failed") {
		t.Fatalf("want rebase failure, got %v", err)
	}
}

func TestFlattenVMChainConvertFailsC458(t *testing.T) {
	c458FakeQemuImg(t, "convert")
	full, inc := c458TwoStepChain(t)
	err := flattenVMChain(context.Background(), []string{full, inc}, filepath.Join(t.TempDir(), "f"))
	if err == nil || !strings.Contains(err.Error(), "qemu-img convert failed") {
		t.Fatalf("want convert failure, got %v", err)
	}
}

func TestFlattenVMChainSidecarCopyFailsC458(t *testing.T) {
	c458FakeQemuImg(t, "")
	full, inc := c458TwoStepChain(t)
	final := filepath.Join(t.TempDir(), "final")
	if err := os.MkdirAll(final, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory squatting on the sidecar's destination name makes the
	// copy fail after the disks have been converted.
	if err := os.Mkdir(filepath.Join(final, "domain.xml"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := flattenVMChain(context.Background(), []string{full, inc}, final)
	if err == nil || !strings.Contains(err.Error(), "copying sidecar") {
		t.Fatalf("want sidecar copy failure, got %v", err)
	}
}

func TestFlattenVMChainFinalDirUncreatableC458(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := flattenVMChain(context.Background(), []string{parent}, filepath.Join(blocker, "final"))
	if err == nil || !strings.Contains(err.Error(), "creating final dir") {
		t.Fatalf("want mkdir failure, got %v", err)
	}
}

func TestFlattenVMChainResolveErrorC458(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, "full")
	inc := filepath.Join(root, "inc")
	writeChainStep(t, full, "", nil)
	writeChainStep(t, inc, "{not json", nil)
	err := flattenVMChain(context.Background(), []string{full, inc}, filepath.Join(root, "final"))
	if err == nil || !strings.Contains(err.Error(), "parsing vm_meta.json") {
		t.Fatalf("want parse failure, got %v", err)
	}
}

func TestFlattenVMChainDisklessMetaCopiesTopC458(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, "full")
	inc := filepath.Join(root, "inc")
	writeChainStep(t, full, `{"disks":[]}`, nil)
	writeChainStep(t, inc, `{"disks":[]}`, map[string]string{"domain.xml": "d"})
	final := filepath.Join(root, "final")
	if err := flattenVMChain(context.Background(), []string{full, inc}, final); err != nil {
		t.Fatalf("diskless chain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(final, "domain.xml")); err != nil {
		t.Fatalf("sidecar missing: %v", err)
	}
}

func TestFlattenVMChainNoDisksResolvedErrorsC458(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, "full")
	inc := filepath.Join(root, "inc")
	// Metadata records only incomplete disks: they are skipped, leaving
	// zero resolved disks although the metadata claims disks exist.
	meta := `{"disks":[{"target":"","backup_file":"x.qcow2"}]}`
	writeChainStep(t, full, meta, nil)
	writeChainStep(t, inc, meta, nil)
	err := flattenVMChain(context.Background(), []string{full, inc}, filepath.Join(root, "final"))
	if err == nil || !strings.Contains(err.Error(), "no VM disk layers resolved") {
		t.Fatalf("want no-disks error, got %v", err)
	}
}

func TestReadVMChainStepMetaRejectsEscapingBackupFileC458(t *testing.T) {
	dir := t.TempDir()
	writeChainStep(t, dir, `{"disks":[{"target":"vda","backup_file":"../evil.qcow2"}]}`, nil)
	if _, err := readVMChainStepMeta(dir); err == nil || !strings.Contains(err.Error(), "invalid backup_file") {
		t.Fatalf("want invalid backup_file error, got %v", err)
	}
}

func TestResolveVMChainDisksTopDirUnreadableC458(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := resolveVMChainDisks([]string{missing}); err == nil || !strings.Contains(err.Error(), "reading top step dir") {
		t.Fatalf("want readdir failure, got %v", err)
	}
}

func TestChainLayerPathForStepBadMetaC458(t *testing.T) {
	dir := t.TempDir()
	writeChainStep(t, dir, "{bad", nil)
	if _, err := chainLayerPathForStep(dir, "vda", "vdisk0"); err == nil {
		t.Fatal("want metadata parse error")
	}
}

func TestChainLayerPathForStepSkipsOtherTargetsC458(t *testing.T) {
	dir := t.TempDir()
	writeChainStep(t, dir,
		`{"disks":[{"target":"vdb","backup_file":"other.qcow2"},{"target":"vda","backup_file":""}]}`,
		map[string]string{"vdisk0.raw": "raw"})
	got, err := chainLayerPathForStep(dir, "vda", "vdisk0")
	if err != nil || filepath.Base(got) != "vdisk0.raw" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := chainLayerPathForStep(dir, "vdc", ""); err == nil {
		t.Fatal("empty base should not resolve to anything")
	}
}

func TestCopyRegularFileSourceIsDirC458(t *testing.T) {
	src := t.TempDir()
	if err := copyRegularFile(src, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("copying a directory should fail")
	}
}

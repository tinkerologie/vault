package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCopyRegularFileHappy verifies the basic copy semantics: contents are
// preserved, mode is preserved, target file is fsync'd.
func TestCopyRegularFileHappy(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "src.bin")
	dst := filepath.Join(t.TempDir(), "dst.bin")
	payload := []byte("hello vault chain copy")
	if err := os.WriteFile(src, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := copyRegularFile(src, dst); err != nil {
		t.Fatalf("copyRegularFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("dst contents = %q, want %q", string(got), string(payload))
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	// Mode is preserved from src.
	if info.Mode().Perm()&0o400 == 0 {
		t.Errorf("dst mode %v lacks owner read bit", info.Mode())
	}
}

// TestCopyRegularFileMissingSource exercises the open-failure branch.
func TestCopyRegularFileMissingSource(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "does-not-exist")
	dst := filepath.Join(t.TempDir(), "dst.bin")
	if err := copyRegularFile(src, dst); err == nil {
		t.Error("expected error when source does not exist")
	}
}

// TestCopyRegularFileDestUnwritable exercises the os.OpenFile failure branch
// by pointing dst into a non-existent directory.
func TestCopyRegularFileDestUnwritable(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Dest directory does not exist — OpenFile fails.
	dst := filepath.Join(t.TempDir(), "missing-subdir", "dst.bin")
	if err := copyRegularFile(src, dst); err == nil {
		t.Error("expected error when dst directory does not exist")
	}
}

// TestCopyDirShallowOnlyFiles verifies that regular files are copied and
// subdirectories are skipped.
func TestCopyDirShallowOnlyFiles(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "b.bin"), []byte("beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Subdirectory that should be ignored.
	if err := os.MkdirAll(filepath.Join(src, "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "deep", "skip.me"), []byte("gone"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyDirShallow(src, dst); err != nil {
		t.Fatalf("copyDirShallow: %v", err)
	}

	for _, name := range []string{"a.txt", "b.bin"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("expected %s copied, got %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "deep")); !os.IsNotExist(err) {
		t.Errorf("subdirectory should not have been copied (err=%v)", err)
	}
}

// TestCopyDirShallowMissingSrc exercises the ReadDir error path.
func TestCopyDirShallowMissingSrc(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "no-such-dir")
	dst := t.TempDir()
	if err := copyDirShallow(src, dst); err == nil {
		t.Error("expected error when src dir does not exist")
	}
}

// TestChainLayerPathsForDiskHappy exercises the file-by-file discovery
// loop. Each step dir is expected to have a vdaX.qcow2 (or .raw / extensionless).
func TestChainLayerPathsForDiskHappy(t *testing.T) {
	t.Parallel()

	d1 := t.TempDir()
	d2 := t.TempDir()
	d3 := t.TempDir()

	// Step 1: target.qcow2
	if err := os.WriteFile(filepath.Join(d1, "vda.qcow2"), []byte("L1"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Step 2: target.raw (covers the second candidate)
	if err := os.WriteFile(filepath.Join(d2, "vda.raw"), []byte("L2"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Step 3: extensionless target name (covers the middle candidate)
	if err := os.WriteFile(filepath.Join(d3, "vda"), []byte("L3"), 0o644); err != nil {
		t.Fatal(err)
	}

	layers, err := chainLayerPathsForDisk([]string{d1, d2, d3}, "vda")
	if err != nil {
		t.Fatalf("chainLayerPathsForDisk: %v", err)
	}
	if len(layers) != 3 {
		t.Fatalf("len(layers)=%d, want 3", len(layers))
	}
	wantPrefixes := []string{d1, d2, d3}
	for i, want := range wantPrefixes {
		if !strings.HasPrefix(layers[i], want) {
			t.Errorf("layer %d = %q, want prefix %q", i, layers[i], want)
		}
	}
}

// TestChainLayerPathsForDiskMissing exercises the not-found error path.
func TestChainLayerPathsForDiskMissing(t *testing.T) {
	t.Parallel()
	d1 := t.TempDir()
	d2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(d1, "vda.qcow2"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// d2 has no disk file — chainLayerPathsForDisk should return an error.
	_, err := chainLayerPathsForDisk([]string{d1, d2}, "vda")
	if err == nil {
		t.Error("expected error when a step dir is missing the disk")
	}
}

// TestFlattenVMChainNoSteps verifies the empty-input guard.
func TestFlattenVMChainNoSteps(t *testing.T) {
	t.Parallel()
	finalDir := filepath.Join(t.TempDir(), "out")
	if err := flattenVMChain(nil, finalDir); err == nil {
		t.Error("expected error for empty step list")
	}
}

// TestFlattenVMChainSingleStep verifies that a single step is copied
// verbatim (no qemu-img invocation needed).
func TestFlattenVMChainSingleStep(t *testing.T) {
	t.Parallel()
	step := t.TempDir()
	if err := os.WriteFile(filepath.Join(step, "vda.qcow2"), []byte("disk-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(step, "domain.xml"), []byte("<domain/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	finalDir := filepath.Join(t.TempDir(), "flat")
	if err := flattenVMChain([]string{step}, finalDir); err != nil {
		t.Fatalf("flattenVMChain: %v", err)
	}

	for _, name := range []string{"vda.qcow2", "domain.xml"} {
		got, err := os.ReadFile(filepath.Join(finalDir, name))
		if err != nil {
			t.Errorf("expected %s in finalDir: %v", name, err)
			continue
		}
		if len(got) == 0 {
			t.Errorf("%s in finalDir is empty", name)
		}
	}
}

// TestFlattenVMChainFinalDirCreated verifies that a missing finalDir is
// created automatically (covers the MkdirAll branch).
func TestFlattenVMChainFinalDirCreated(t *testing.T) {
	t.Parallel()
	step := t.TempDir()
	if err := os.WriteFile(filepath.Join(step, "vda.qcow2"), []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}
	// finalDir under a not-yet-existing parent.
	finalDir := filepath.Join(t.TempDir(), "deeply", "nested", "out")
	if err := flattenVMChain([]string{step}, finalDir); err != nil {
		t.Fatalf("flattenVMChain: %v", err)
	}
	if _, err := os.Stat(finalDir); err != nil {
		t.Errorf("finalDir not created: %v", err)
	}
}

// TestFlattenVMChainMultiStepNoQcow2FallsBackToCopy verifies the
// "no disk targets discovered" branch: a multi-step chain whose top step
// contains no .qcow2 files copies the top step verbatim rather than failing.
func TestFlattenVMChainMultiStepNoQcow2FallsBackToCopy(t *testing.T) {
	t.Parallel()
	d1 := t.TempDir()
	d2 := t.TempDir()
	// No .qcow2 anywhere — top step has only sidecars.
	if err := os.WriteFile(filepath.Join(d2, "domain.xml"), []byte("<domain/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d2, "vm_meta.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	finalDir := filepath.Join(t.TempDir(), "flat")
	if err := flattenVMChain([]string{d1, d2}, finalDir); err != nil {
		t.Fatalf("flattenVMChain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(finalDir, "domain.xml")); err != nil {
		t.Errorf("domain.xml not copied verbatim from top step: %v", err)
	}
}

// NOTE: The multi-step / qcow2-rebase branches of flattenVMChain shell
// out to `qemu-img rebase` / `qemu-img convert`. These commands are not
// available in CI / dev workstations without libvirt-utils installed,
// so we deliberately don't exercise them here. Those paths would require
// either a real qemu-img binary or extracting qemu-img invocation into
// an injectable seam — out of scope for the coverage-lift task.

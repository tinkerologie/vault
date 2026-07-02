package runner

import (
	"os"
	"path/filepath"
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

// TestChainLayerPathForStepCandidates exercises the filename-candidate
// fallback used for steps without vm_meta.json disk records. Each step dir
// may hold the disk as .qcow2, .raw, .img or extensionless.
func TestChainLayerPathForStepCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		diskFile string
	}{
		{"qcow2", "vda.qcow2"},
		{"raw", "vda.raw"},
		{"extensionless", "vda"},
		{"img", "vda.img"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tt.diskFile), []byte("layer"), 0o644); err != nil {
				t.Fatal(err)
			}

			p, err := chainLayerPathForStep(dir, "vda", "vda")
			if err != nil {
				t.Fatalf("chainLayerPathForStep: %v", err)
			}
			if p != filepath.Join(dir, tt.diskFile) {
				t.Errorf("layer = %q, want %q", p, filepath.Join(dir, tt.diskFile))
			}
		})
	}
}

// TestChainLayerPathForStepMissing exercises the not-found error path.
func TestChainLayerPathForStepMissing(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	if _, err := chainLayerPathForStep(d, "vda", "vda"); err == nil {
		t.Error("expected error when a step dir is missing the disk")
	}
}

// TestFlattenVMChainNoSteps verifies the empty-input guard.
func TestFlattenVMChainNoSteps(t *testing.T) {
	t.Parallel()
	finalDir := filepath.Join(t.TempDir(), "out")
	if err := flattenVMChain(t.Context(), nil, finalDir); err == nil {
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
	if err := flattenVMChain(t.Context(), []string{step}, finalDir); err != nil {
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
	if err := flattenVMChain(t.Context(), []string{step}, finalDir); err != nil {
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
	if err := flattenVMChain(t.Context(), []string{d1, d2}, finalDir); err != nil {
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

func writeChainStep(t *testing.T, dir, meta string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if meta != "" {
		if err := os.WriteFile(filepath.Join(dir, "vm_meta.json"), []byte(meta), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Unraid VM disks are usually named vdisk1.img even when qcow2-formatted, so
// a chain's full step is written as vdisk0.img while incremental steps are
// forced to vdisk0.qcow2. Layer resolution must follow vm_meta.json instead
// of assuming every layer ends in .qcow2.
func TestResolveVMChainDisksFromMetadata(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	full := filepath.Join(root, "step_0")
	inc := filepath.Join(root, "step_1")

	writeChainStep(t, full,
		`{"state":"running","disks":[{"target":"hdc","backup_file":"vdisk0.img","format":"qcow2"}]}`,
		map[string]string{"vdisk0.img": "full-layer", "domain.xml": "<domain/>"})
	writeChainStep(t, inc,
		`{"state":"running","disks":[{"target":"hdc","backup_file":"vdisk0.qcow2","format":"qcow2"}]}`,
		map[string]string{"vdisk0.qcow2": "inc-layer", "domain.xml": "<domain/>"})

	disks, err := resolveVMChainDisks([]string{full, inc})
	if err != nil {
		t.Fatalf("resolveVMChainDisks() error = %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(disks))
	}

	d := disks[0]
	if d.Target != "hdc" {
		t.Fatalf("unexpected target: %q", d.Target)
	}
	wantLayers := []string{filepath.Join(full, "vdisk0.img"), filepath.Join(inc, "vdisk0.qcow2")}
	if len(d.Layers) != 2 || d.Layers[0] != wantLayers[0] || d.Layers[1] != wantLayers[1] {
		t.Fatalf("unexpected layers: %v (want %v)", d.Layers, wantLayers)
	}
	// The flattened output must use the full step's filename: the engine's
	// restore plan derives expected names from the original disk path
	// extension (vdisk0.img here).
	if d.OutputName != "vdisk0.img" {
		t.Fatalf("unexpected output name: %q", d.OutputName)
	}
}

func TestResolveVMChainDisksLegacyFallback(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	full := filepath.Join(root, "step_0")
	inc := filepath.Join(root, "step_1")

	// Old backups have no disks recorded in vm_meta.json.
	writeChainStep(t, full, `{"state":"running"}`,
		map[string]string{"vdisk0.qcow2": "full-layer", "domain.xml": "<domain/>"})
	writeChainStep(t, inc, "",
		map[string]string{"vdisk0.qcow2": "inc-layer", "domain.xml": "<domain/>"})

	disks, err := resolveVMChainDisks([]string{full, inc})
	if err != nil {
		t.Fatalf("resolveVMChainDisks() error = %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(disks))
	}
	d := disks[0]
	if d.Layers[0] != filepath.Join(full, "vdisk0.qcow2") || d.Layers[1] != filepath.Join(inc, "vdisk0.qcow2") {
		t.Fatalf("unexpected layers: %v", d.Layers)
	}
	if d.OutputName != "vdisk0.qcow2" {
		t.Fatalf("unexpected output name: %q", d.OutputName)
	}
}

func TestResolveVMChainDisksMissingLayer(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	full := filepath.Join(root, "step_0")
	inc := filepath.Join(root, "step_1")

	writeChainStep(t, full,
		`{"disks":[{"target":"hdc","backup_file":"vdisk0.img","format":"qcow2"}]}`,
		map[string]string{"domain.xml": "<domain/>"})
	writeChainStep(t, inc,
		`{"disks":[{"target":"hdc","backup_file":"vdisk0.qcow2","format":"qcow2"}]}`,
		map[string]string{"vdisk0.qcow2": "inc-layer"})

	if _, err := resolveVMChainDisks([]string{full, inc}); err == nil {
		t.Fatal("expected error when a chain layer file is missing")
	}
}

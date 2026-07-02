package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// vmChainStepMeta mirrors the disk-related subset of the engine's
// vm_meta.json schema so chain layers can be resolved by recorded filename
// instead of extension guessing (full steps keep the source extension, e.g.
// vdisk0.img, while incremental steps are always vdisk0.qcow2).
type vmChainStepMeta struct {
	Disks []struct {
		Target     string `json:"target"`
		BackupFile string `json:"backup_file"`
		Format     string `json:"format"`
	} `json:"disks"`
}

func readVMChainStepMeta(dir string) (vmChainStepMeta, error) {
	data, err := os.ReadFile(filepath.Join(dir, "vm_meta.json")) // #nosec G304 — vault-controlled staging dir
	if err != nil {
		return vmChainStepMeta{}, fmt.Errorf("reading vm_meta.json in %s: %w", dir, err)
	}
	var meta vmChainStepMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return vmChainStepMeta{}, fmt.Errorf("parsing vm_meta.json in %s: %w", dir, err)
	}
	for _, d := range meta.Disks {
		// backup_file is an in-step filename; reject anything that could
		// escape the step directory (the metadata travels inside backup
		// archives and must not be trusted as a path).
		if d.BackupFile != "" && d.BackupFile != filepath.Base(d.BackupFile) {
			return vmChainStepMeta{}, fmt.Errorf("invalid backup_file %q in vm_meta.json in %s", d.BackupFile, dir)
		}
	}
	return meta, nil
}

// vmChainDisk is one disk's resolved restore chain: ordered layer paths
// (full first) plus the filename the flattened output must use — the full
// step's backup file, because the engine's restore plan derives expected
// names from the original disk path extension.
type vmChainDisk struct {
	Target     string
	Layers     []string
	OutputName string
}

// resolveVMChainDisks maps each disk of a VM chain to its per-step layer
// files. It prefers the vm_meta.json disk records written by the engine and
// falls back to filename candidates for steps from older Vault versions.
func resolveVMChainDisks(stepDirs []string) ([]vmChainDisk, error) {
	topDir := stepDirs[len(stepDirs)-1]

	type diskKey struct{ target, base string }
	var keys []diskKey
	meta, metaErr := readVMChainStepMeta(topDir)
	if metaErr != nil && !errors.Is(metaErr, os.ErrNotExist) {
		// A present-but-unreadable vm_meta.json signals a damaged step;
		// falling back to filename guessing could assemble a wrong chain.
		return nil, metaErr
	}
	if metaErr == nil && len(meta.Disks) > 0 {
		for _, d := range meta.Disks {
			base := strings.TrimSuffix(d.BackupFile, filepath.Ext(d.BackupFile))
			keys = append(keys, diskKey{target: d.Target, base: base})
		}
	} else {
		// Legacy fallback (no metadata, or metadata without disk records):
		// scan the top step for qcow2 disk files.
		topEntries, err := os.ReadDir(topDir)
		if err != nil {
			return nil, fmt.Errorf("reading top step dir: %w", err)
		}
		for _, e := range topEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".qcow2") {
				continue
			}
			base := strings.TrimSuffix(e.Name(), ".qcow2")
			keys = append(keys, diskKey{target: base, base: base})
		}
	}

	disks := make([]vmChainDisk, 0, len(keys))
	for _, key := range keys {
		layers := make([]string, 0, len(stepDirs))
		for _, dir := range stepDirs {
			path, err := chainLayerPathForStep(dir, key.target, key.base)
			if err != nil {
				return nil, err
			}
			layers = append(layers, path)
		}
		disks = append(disks, vmChainDisk{
			Target:     key.target,
			Layers:     layers,
			OutputName: filepath.Base(layers[0]),
		})
	}
	return disks, nil
}

// chainLayerPathForStep locates one disk's backup file inside a single chain
// step, preferring the step's own vm_meta.json record over filename
// candidates.
func chainLayerPathForStep(dir, target, base string) (string, error) {
	meta, metaErr := readVMChainStepMeta(dir)
	if metaErr != nil && !errors.Is(metaErr, os.ErrNotExist) {
		return "", metaErr
	}
	if metaErr == nil {
		for _, d := range meta.Disks {
			if d.Target != target || d.BackupFile == "" {
				continue
			}
			p := filepath.Join(dir, d.BackupFile)
			// Lstat: reject symlinks planted in the staging dir so qemu-img
			// only ever operates on regular files vault staged itself.
			if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
				return p, nil
			}
			return "", fmt.Errorf("backup file %s recorded for disk %s is missing in %s", d.BackupFile, target, dir)
		}
	}

	for _, name := range []string{base + ".qcow2", base, base + ".raw", base + ".img"} {
		if name == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no backup file found for disk %s in %s", target, dir)
}

// flattenVMChain assembles an ordered set of VM chain step directories
// (stepDirs[0] = full backup; stepDirs[len-1] = most recent
// incremental/differential) into a single self-contained restore directory at
// finalDir. For each disk found in the top step, it links the qcow2 chain
// using `qemu-img rebase -u` and then materialises a single flattened qcow2
// in finalDir using `qemu-img convert -O qcow2`.
//
// Sidecar files (domain.xml, vm_meta.json, NVRAM, etc.) are taken from the
// top step verbatim. qemu-img must be available on PATH.
//
// This intentionally lives in the runner package: the engine package is
// architecturally constrained to be pure-Go (no shelling out to virsh /
// qemu-img). Chain assembly is an orchestration concern, so it sits here.
func flattenVMChain(ctx context.Context, stepDirs []string, finalDir string) error {
	if len(stepDirs) == 0 {
		return fmt.Errorf("no chain steps")
	}
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		return fmt.Errorf("creating final dir: %w", err)
	}

	topDir := stepDirs[len(stepDirs)-1]

	// Single full-backup case — just copy the top step verbatim.
	if len(stepDirs) == 1 {
		return copyDirShallow(topDir, finalDir)
	}

	// Resolve each disk's chain layers (vm_meta.json driven, with a
	// filename fallback for older backups).
	disks, err := resolveVMChainDisks(stepDirs)
	if err != nil {
		return err
	}
	if len(disks) == 0 {
		// A diskless VM's chain legitimately carries only sidecars; its
		// metadata records zero disks. Anything else means discovery
		// failed and a verbatim top-step copy would silently drop the
		// lower chain steps.
		if meta, metaErr := readVMChainStepMeta(topDir); metaErr == nil && len(meta.Disks) == 0 {
			return copyDirShallow(topDir, finalDir)
		}
		return fmt.Errorf("no VM disk layers resolved in chain top step %s", topDir)
	}

	for _, disk := range disks {
		layers := disk.Layers
		for i := 1; i < len(layers); i++ {
			parentAbs, err := filepath.Abs(layers[i-1])
			if err != nil {
				return fmt.Errorf("resolving parent path: %w", err)
			}
			// Chains only exist for all-qcow2 VMs (raw disks force full
			// backups), so every layer's content is qcow2 regardless of
			// its filename extension.
			cmd := exec.CommandContext(ctx, "qemu-img", "rebase", "-u", "-F", "qcow2", "-b", parentAbs, layers[i]) // #nosec G204 — fixed binary; paths from vault-controlled staging dir
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("qemu-img rebase failed for %s: %w (%s)", layers[i], err, string(out))
			}
		}

		dest := filepath.Join(finalDir, disk.OutputName)
		cmd := exec.CommandContext(ctx, "qemu-img", "convert", "-O", "qcow2", layers[len(layers)-1], dest) // #nosec G204 — fixed binary; paths from vault-controlled staging dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("qemu-img convert failed for %s: %w (%s)", disk.Target, err, string(out))
		}
	}

	// Copy non-disk sidecars (domain.xml, vm_meta.json, NVRAM, etc.) from
	// the top step.
	skip := make(map[string]struct{}, len(disks)*2)
	for _, disk := range disks {
		skip[filepath.Base(disk.Layers[len(disk.Layers)-1])] = struct{}{}
		skip[disk.OutputName] = struct{}{}
	}
	topEntries, err := os.ReadDir(topDir)
	if err != nil {
		return fmt.Errorf("reading top step dir: %w", err)
	}
	for _, e := range topEntries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if _, isDisk := skip[name]; isDisk {
			continue
		}
		if err := copyRegularFile(filepath.Join(topDir, name), filepath.Join(finalDir, name)); err != nil {
			return fmt.Errorf("copying sidecar %s: %w", name, err)
		}
	}
	return nil
}

func copyDirShallow(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyRegularFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 — vault-controlled staging dir
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode()) // #nosec G304 — vault-controlled staging dir
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

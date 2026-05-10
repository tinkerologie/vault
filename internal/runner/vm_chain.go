package runner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
func flattenVMChain(stepDirs []string, finalDir string) error {
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

	// Discover disk targets by scanning the top step for vdisk*.qcow2 files.
	// This avoids parsing libvirt domain.xml in the runner.
	topEntries, err := os.ReadDir(topDir)
	if err != nil {
		return fmt.Errorf("reading top step dir: %w", err)
	}
	var diskTargets []string
	for _, e := range topEntries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".qcow2") {
			continue
		}
		base := strings.TrimSuffix(name, ".qcow2")
		// Heuristic: vault writes backup disks as <target>.qcow2 / <target>.<format>
		// where target is something like "vda", "vdb", "hda", "sda", "vdisk0".
		// Skip anything that looks like a non-disk (e.g. a directory name).
		diskTargets = append(diskTargets, base)
	}
	if len(diskTargets) == 0 {
		// Fall back: just copy top step as-is (full backup with no qcow2s
		// or a non-qcow2 chain we can't merge). Better than failing entirely.
		return copyDirShallow(topDir, finalDir)
	}

	for _, target := range diskTargets {
		layers, err := chainLayerPathsForDisk(stepDirs, target)
		if err != nil {
			return err
		}
		if len(layers) != len(stepDirs) {
			return fmt.Errorf("incomplete chain for disk %s: have %d/%d layers", target, len(layers), len(stepDirs))
		}

		for i := 1; i < len(layers); i++ {
			parentAbs, err := filepath.Abs(layers[i-1])
			if err != nil {
				return fmt.Errorf("resolving parent path: %w", err)
			}
			cmd := exec.Command("qemu-img", "rebase", "-u", "-F", "qcow2", "-b", parentAbs, layers[i]) // #nosec G204 — fixed binary; paths from vault-controlled staging dir
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("qemu-img rebase failed for %s: %w (%s)", layers[i], err, string(out))
			}
		}

		dest := filepath.Join(finalDir, target+".qcow2")
		cmd := exec.Command("qemu-img", "convert", "-O", "qcow2", layers[len(layers)-1], dest) // #nosec G204 — fixed binary; paths from vault-controlled staging dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("qemu-img convert failed for %s: %w (%s)", target, err, string(out))
		}
	}

	// Copy non-disk sidecars (domain.xml, vm_meta.json, NVRAM, etc.) from
	// the top step.
	skip := make(map[string]struct{}, len(diskTargets)*2)
	for _, t := range diskTargets {
		skip[t+".qcow2"] = struct{}{}
		skip[t] = struct{}{}
		skip[t+".raw"] = struct{}{}
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

func chainLayerPathsForDisk(stepDirs []string, target string) ([]string, error) {
	layers := make([]string, 0, len(stepDirs))
	candidates := []string{target + ".qcow2", target, target + ".raw"}
	for _, dir := range stepDirs {
		var found string
		for _, name := range candidates {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err == nil {
				found = p
				break
			}
		}
		if found == "" {
			return nil, fmt.Errorf("no backup file found for disk %s in %s", target, dir)
		}
		layers = append(layers, found)
	}
	return layers, nil
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

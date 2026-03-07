//go:build linux

package engine

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// qcow2Info holds fields from `qemu-img info --output=json`.
type qcow2Info struct {
	BackingFilename string `json:"backing-filename"`
}

// qcow2HasBacking returns true if the image at path is a qcow2 file with
// a backing-file reference (i.e. it is an overlay in a chain).
func qcow2HasBacking(path string) (bool, error) {
	out, err := exec.Command("qemu-img", "info", "--output=json", path).Output()
	if err != nil {
		return false, fmt.Errorf("qemu-img info %s: %w", path, err)
	}

	var info qcow2Info
	if err := json.Unmarshal(out, &info); err != nil {
		return false, fmt.Errorf("parsing qemu-img info output: %w", err)
	}

	return info.BackingFilename != "", nil
}

// qcow2Flatten converts a qcow2 image with a backing chain into a
// standalone qcow2 file at dst by merging all layers.
func qcow2Flatten(src, dst string) error {
	cmd := exec.Command("qemu-img", "convert", "-O", "qcow2", src, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("flattening qcow2 %s: %s: %w", src, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// copyOrFlattenDisk copies a disk image from src to dst. If the source is
// a qcow2 overlay that depends on a backing file, it flattens the chain
// into a standalone image so the backup is self-contained.
func copyOrFlattenDisk(src, dst string, onProgress func(int64)) error {
	hasBacking, err := qcow2HasBacking(src)
	if err != nil {
		// If qemu-img is unavailable or the file isn't qcow2, fall back
		// to a regular copy. This preserves existing behaviour.
		return copyFileWithProgress(src, dst, onProgress)
	}

	if !hasBacking {
		return copyFileWithProgress(src, dst, onProgress)
	}

	// Flatten the qcow2 chain into a standalone image.
	return qcow2Flatten(src, dst)
}

package handlers

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/safepath"
)

var browseAllowedRoots = []string{"/mnt", "/boot"}

var configurablePathRoots = []string{"/mnt", "/boot", "/tmp"}

func normalizeConfigurablePath(path string) (string, error) {
	return safepath.NormalizeAbsoluteUnderRoots(path, configurablePathRoots)
}

func normalizeRestoreDestination(path string) (string, error) {
	if strings.Contains(path, "../") || strings.Contains(path, "..\\") {
		return "", fmt.Errorf("path traversal not allowed")
	}
	if slices.Contains(strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	}), "..") {
		return "", fmt.Errorf("path traversal not allowed")
	}
	return safepath.NormalizeAbsoluteUnderRoots(path, safepath.RestoreAllowedRoots())
}

// restoreDestinationFor validates a restore destination for its items. A ZFS
// restore receives into a dataset, named as zfs names it; every other item
// restores to an absolute path under the allowed roots.
func restoreDestinationFor(dest string, zfs bool) (string, error) {
	if zfs {
		if !engine.ValidZFSDatasetName(dest) {
			return "", fmt.Errorf("%q is not a valid ZFS dataset name", dest)
		}
		return dest, nil
	}
	return normalizeRestoreDestination(dest)
}

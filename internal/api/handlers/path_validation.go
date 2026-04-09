package handlers

import "github.com/ruaan-deysel/vault/internal/safepath"

var browseAllowedRoots = []string{"/mnt", "/boot"}

var configurablePathRoots = []string{"/mnt", "/boot", "/tmp"}

func normalizeConfigurablePath(path string) (string, error) {
	return safepath.NormalizeAbsoluteUnderRoots(path, configurablePathRoots)
}

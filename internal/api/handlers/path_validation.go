package handlers

import "github.com/ruaandeysel/vault/internal/safepath"

var browseAllowedRoots = []string{"/mnt", "/boot"}

var configurablePathRoots = []string{"/mnt", "/boot", "/tmp"}

func normalizeBrowsePath(path string) (string, error) {
	return safepath.NormalizeAbsoluteUnderRoots(path, browseAllowedRoots)
}

func normalizeConfigurablePath(path string) (string, error) {
	return safepath.NormalizeAbsoluteUnderRoots(path, configurablePathRoots)
}

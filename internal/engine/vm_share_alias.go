package engine

import (
	"path/filepath"
	"strings"
)

// unraidShareKey returns the share-relative identity ("<share>/<rest>") of a
// file under an Unraid array or pool mount, or "" for any other path. The
// same file can be named as /mnt/user/<share>/p, /mnt/user0/<share>/p,
// /mnt/cache/<share>/p, /mnt/<pool>/<share>/p or /mnt/diskN/<share>/p, so
// those spellings must compare equal. Unassigned devices, remote mounts and
// the root share do not belong to the user shares.
func unraidShareKey(path string) string {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return ""
	}
	parts := strings.SplitN(strings.TrimPrefix(clean, "/"), "/", 4)
	if len(parts) < 4 || parts[0] != "mnt" {
		return ""
	}
	switch parts[1] {
	case "remotes", "disks", "addons", "rootshare":
		return ""
	}
	return parts[2] + "/" + parts[3]
}

// vmTargetKeys lists the identities a host path is known by. Share keys carry
// a prefix that no absolute path can start with.
func vmTargetKeys(path string) []string {
	keys := []string{filepath.Clean(path)}
	if share := unraidShareKey(path); share != "" {
		keys = append(keys, "unraid-share:"+share)
	}
	return keys
}

func addVMTarget(targets map[string]bool, path string) {
	for _, key := range vmTargetKeys(path) {
		targets[key] = true
	}
}

func vmTargetInSet(targets map[string]bool, path string) bool {
	for _, key := range vmTargetKeys(path) {
		if targets[key] {
			return true
		}
	}
	return false
}

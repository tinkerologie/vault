package engine

import (
	"path/filepath"
	"strings"
)

// unraidShareKey returns the share-relative identity ("<share>/<rest>") of a
// file under an Unraid array or pool mount, or "" for any other path, and
// reports whether the path is a user-share view (/mnt/user or /mnt/user0).
// Unassigned devices, remote mounts and the root share do not belong to the
// user shares.
func unraidShareKey(path string) (string, bool) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(clean, "/"), "/", 4)
	if len(parts) < 4 || parts[0] != "mnt" {
		return "", false
	}
	switch parts[1] {
	case "remotes", "disks", "addons", "rootshare":
		return "", false
	}
	return parts[2] + "/" + parts[3], parts[1] == "user" || parts[1] == "user0"
}

// Share keys carry prefixes that no absolute path can start with.
const (
	vmUserShareKey     = "unraid-user:"
	vmPhysicalShareKey = "unraid-disk:"
)

// addVMTarget records path under its own name and its share identity. A user
// share view (/mnt/user/<share>/p, /mnt/user0/<share>/p) and a disk or pool
// path (/mnt/cache/<share>/p, /mnt/diskN/<share>/p) can name the same file,
// but two different disks or pools never do.
func addVMTarget(targets map[string]bool, path string) {
	targets[filepath.Clean(path)] = true
	if share, user := unraidShareKey(path); share != "" {
		if user {
			targets[vmUserShareKey+share] = true
		} else {
			targets[vmPhysicalShareKey+share] = true
		}
	}
}

// vmTargetInSet reports whether path names a file recorded by addVMTarget:
// the same path, or a user-share view and another spelling of the same share
// file where at least one side is the user-share view.
func vmTargetInSet(targets map[string]bool, path string) bool {
	if targets[filepath.Clean(path)] {
		return true
	}
	share, user := unraidShareKey(path)
	if share == "" {
		return false
	}
	if targets[vmUserShareKey+share] {
		return true
	}
	return user && targets[vmPhysicalShareKey+share]
}

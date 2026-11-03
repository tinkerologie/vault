package engine

import (
	"fmt"
	"strings"
)

// zfsMaxNameLen is OpenZFS's ZFS_MAX_DATASET_NAME_LEN, which counts the NUL.
const zfsMaxNameLen = 256

// validZFSDatasetName applies OpenZFS's rules for a filesystem or volume name
// (module/zcommon/zfs_namecheck.c): components of letters, digits and
// "-_.: ", none empty or exactly "." or "..", and a pool name that starts with
// a letter and is not reserved. "@" and "#" are excluded because a receive
// target is never a snapshot or bookmark, and "%" is reserved internally.
func validZFSDatasetName(name string) bool {
	if name == "" || len(name) >= zfsMaxNameLen {
		return false
	}
	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return false
		}
		for _, r := range p {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
				r == '-' || r == '_' || r == '.' || r == ':' || r == ' '
			if !ok {
				return false
			}
		}
	}
	return validZFSPoolName(parts[0])
}

// validZFSPoolName applies zpool's extra rules (pool_namecheck): a leading
// letter, no vdev-type prefix, and not a Solaris-style disk name like c0t0d0.
func validZFSPoolName(pool string) bool {
	if !isASCIILetter(pool[0]) {
		return false
	}
	for _, reserved := range []string{"mirror", "raidz", "draid"} {
		if strings.HasPrefix(pool, reserved) {
			return false
		}
	}
	if pool == "spare" || pool == "log" {
		return false
	}
	isDiskName := pool[0] == 'c' && len(pool) > 1 && pool[1] >= '0' && pool[1] <= '9'
	return !isDiskName
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// ZFSRestoreTarget is zfsRestoreTarget for callers that build a restore's
// settings, so they can check the target the handler will choose.
func ZFSRestoreTarget(settings map[string]any) (string, error) { return zfsRestoreTarget(settings) }

// zfsRestoreTarget returns the dataset a ZFS restore may force-receive into.
// Only two values authorise a target: a destination the user chose for this
// restore, and the dataset configured on the job's item. A pool root takes
// every child dataset with it under "recv -F", so it must be chosen explicitly.
func zfsRestoreTarget(settings map[string]any) (string, error) {
	target, _ := settings["restore_destination"].(string)
	explicit := target != ""
	if !explicit {
		target, _ = settings["dataset"].(string)
	}
	if target == "" {
		return "", fmt.Errorf("no target dataset is configured: add the dataset to this job's item, then restore again")
	}
	if !validZFSDatasetName(target) {
		return "", fmt.Errorf("invalid target dataset %q", target)
	}
	if !explicit && !strings.Contains(target, "/") {
		return "", fmt.Errorf("refusing to overwrite pool root %q without an explicit restore destination", target)
	}
	return target, nil
}

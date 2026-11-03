package engine

import (
	"fmt"
	"regexp"
	"strings"
)

// zfsDatasetName matches a complete filesystem or volume name. The first
// character rules out anything zfs would parse as an option, and "@" and "#"
// are excluded because a receive target is never a snapshot or bookmark.
var zfsDatasetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*(/[A-Za-z0-9_.: -]+)*$`)

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
	if !zfsDatasetName.MatchString(target) || strings.Contains(target, "..") {
		return "", fmt.Errorf("invalid target dataset %q", target)
	}
	if !explicit && !strings.Contains(target, "/") {
		return "", fmt.Errorf("refusing to overwrite pool root %q without an explicit restore destination", target)
	}
	return target, nil
}

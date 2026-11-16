//go:build unix

package fsstat

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Stat returns the capacity of the filesystem that holds path.
func Stat(path string) (Usage, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return Usage{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	bsize := uint64(s.Bsize) //nolint:gosec,unconvert // Bsize is uint32 on Darwin and int64 on Linux; never negative
	return Usage{
		Total: uint64(s.Blocks) * bsize, //nolint:unconvert // uint64 on Linux, may differ elsewhere
		Free:  uint64(s.Bavail) * bsize, //nolint:unconvert // uint64 on Linux, may differ elsewhere
	}, nil
}

// SameFilesystem compares the device IDs of two existing local paths.
func SameFilesystem(first, second string) (bool, error) {
	var a, b unix.Stat_t
	if err := unix.Stat(first, &a); err != nil {
		return false, fmt.Errorf("stat %s: %w", first, err)
	}
	if err := unix.Stat(second, &b); err != nil {
		return false, fmt.Errorf("stat %s: %w", second, err)
	}
	return a.Dev == b.Dev, nil
}

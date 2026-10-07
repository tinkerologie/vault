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

//go:build unix

package engine

import (
	"errors"
	"os"
	"syscall"
)

// isSymlinkErr reports whether err indicates an open failed because the target
// is a symlink (O_NOFOLLOW). On Linux, O_DIRECTORY on a symlink returns ENOTDIR.
func isSymlinkErr(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR)
}

// fileOwner returns the numeric owner of a stat result. The (-1, -1) pair
// means "unknown" — chown treats a negative id as "leave unchanged", so it is
// also the value applyOwner skips on.
func fileOwner(info os.FileInfo) (uid, gid int) {
	if info == nil {
		return -1, -1
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1
	}
	return int(st.Uid), int(st.Gid)
}

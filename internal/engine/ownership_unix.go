//go:build unix

package engine

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// openNoFollow is O_NOFOLLOW on unix platforms to prevent symlink traversal.
const openNoFollow = syscall.O_NOFOLLOW

// isSymlinkErr reports whether err indicates an open failed because the target
// is a symlink (O_NOFOLLOW). On Linux, O_DIRECTORY on a symlink returns ENOTDIR.
func isSymlinkErr(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR)
}

// chtimesNoFollow updates the modification time without following a symlink.
func chtimesNoFollow(path string, t time.Time) error {
	ts := unix.NsecToTimespec(t.UnixNano())
	times := []unix.Timespec{ts, ts}
	return unix.UtimesNanoAt(unix.AT_FDCWD, path, times, unix.AT_SYMLINK_NOFOLLOW)
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

// openRestoreFile opens path for a restore write. It refuses anything but a
// regular file: without O_NONBLOCK a FIFO planted at the path would hang the
// restore or hand restored data to whoever reads it. The type is checked on the
// open descriptor, and truncation waits for that check, so there is no race.
func openRestoreFile(path string, mode os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|syscall.O_NONBLOCK|openNoFollow, mode) // #nosec G304 //nolint:gosec // callers validate path; the final component is pinned by openNoFollow
	if err != nil {
		if errors.Is(err, syscall.ENXIO) { // FIFO with no reader, or a device with no driver.
			return nil, fmt.Errorf("refusing to restore to non-regular file %s", path)
		}
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("refusing to restore to non-regular file %s (%s)", path, info.Mode().Type())
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

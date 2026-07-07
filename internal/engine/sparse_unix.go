//go:build unix

package engine

import (
	"os"
	"syscall"
)

// sparseInfo reports whether a large regular file's logical size vastly exceeds
// its physical on-disk size (a sparse file) and returns the physical byte count.
// No-op (false, 0) for small/non-regular files or when block info is
// unavailable. st_blocks is always in 512-byte units (POSIX).
func sparseInfo(info os.FileInfo) (sparse bool, physicalBytes int64) {
	if info == nil || !info.Mode().IsRegular() || info.Size() < sparseWarnMinBytes {
		return false, 0
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, 0
	}
	physical := st.Blocks * 512
	if physical == 0 {
		// A large file with zero allocated blocks is fully sparse (all holes).
		return true, 0
	}
	return info.Size()/physical >= sparseWarnRatio, physical
}

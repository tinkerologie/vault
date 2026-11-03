//go:build unix

package db

import (
	"os"
	"syscall"
)

// ownedByProcess reports whether a file belongs to this process's user.
func ownedByProcess(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

//go:build unix

package tempdir

import (
	"os"
	"syscall"
)

// ownedByDaemon reports whether info belongs to the user this process runs as.
func ownedByDaemon(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

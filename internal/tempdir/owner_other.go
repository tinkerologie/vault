//go:build !unix

package tempdir

import "os"

// ownedByDaemon has no ownership model to check off unix.
func ownedByDaemon(os.FileInfo) bool { return true }

//go:build !unix

package engine

import "os"

// fileOwner has no meaning off unix: there is no numeric uid/gid to record, so
// every caller sees "unknown" and ownership is left to the filesystem.
func fileOwner(_ os.FileInfo) (uid, gid int) { return -1, -1 }

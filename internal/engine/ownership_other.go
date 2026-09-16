//go:build !unix

package engine

import "os"

// openNoFollow is 0 on non-unix platforms where O_NOFOLLOW is not defined.
const openNoFollow = 0

// fileOwner has no meaning off unix: there is no numeric uid/gid to record, so
// every caller sees "unknown" and ownership is left to the filesystem.
func fileOwner(_ os.FileInfo) (uid, gid int) { return -1, -1 }

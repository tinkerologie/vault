package storage

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
)

// unraidUserShareRoot is the mount point of Unraid's shfs union filesystem.
// Paths under it are the ones where "no space left on device" is misleading:
// the array can report terabytes free while the write still fails.
const unraidUserShareRoot = "/mnt/user"

// IsNoSpace reports whether err was caused by the destination filesystem
// running out of space. Unwraps *os.PathError / *os.SyscallError, so it works
// on errors surfaced by io.Copy, Sync, and Rename alike.
func IsNoSpace(err error) bool {
	return err != nil && errors.Is(err, syscall.ENOSPC)
}

// NoSpaceError wraps a disk-full failure with the directory that filled and,
// on an Unraid user share, an explanation of why the array's free-space figure
// does not apply.
//
// The bare OS message ("no space left on device") sends Unraid operators
// looking for permission problems or doubting a share with terabytes free,
// because shfs spreads a share across disks but writes any single file to one
// of them — so a large VM disk image fails as soon as the chosen disk is full,
// regardless of the array total (issue #255).
func NoSpaceError(dir string, err error) error {
	msg := fmt.Sprintf("no space left on the volume backing %s", dir)
	if isUnraidUserShare(dir) {
		// Deliberately covers both shapes a /mnt/user path can take. An
		// array-backed share writes each file to one disk, so the array total
		// is not the limit; a cache-only or pool-backed share lives entirely
		// on that pool. The path alone cannot distinguish them — shfs is a
		// FUSE mount, so the name does not resolve to the backing device —
		// and naming only the array case would misdirect someone whose cache
		// pool is what actually filled.
		msg += ". This is an Unraid user share, so the array's total free space" +
			" is not what limits this write: an array-backed share puts each" +
			" file on a single disk, and a cache-only or pool-backed share is" +
			" limited by that pool. Check free space on the disk or pool this" +
			" share actually uses, review the share's Minimum Free Space and" +
			" Split Level settings, or point this destination at a specific" +
			" disk or pool (for example /mnt/disk1 or /mnt/cache) instead of" +
			" /mnt/user"
	}
	return fmt.Errorf("%s: %w", msg, err)
}

// isUnraidUserShare reports whether dir lives under /mnt/user. Cleaned first so
// "/mnt/user/../userfoo" cannot masquerade as a share path.
func isUnraidUserShare(dir string) bool {
	clean := filepath.Clean(dir)
	return clean == unraidUserShareRoot || strings.HasPrefix(clean, unraidUserShareRoot+string(filepath.Separator))
}

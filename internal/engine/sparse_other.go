//go:build !unix

package engine

import "os"

// sparseInfo is a no-op on non-Unix platforms, where os.FileInfo.Sys() does not
// expose the on-disk block count needed to detect a sparse file.
func sparseInfo(os.FileInfo) (sparse bool, physicalBytes int64) {
	return false, 0
}

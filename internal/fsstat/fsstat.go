// Package fsstat reports filesystem capacity for a local path in a
// platform-neutral way. It is the single home for the statfs (Unix) and
// GetDiskFreeSpaceEx (Windows) calls so callers stay portable (issue #313).
package fsstat

// Usage is the capacity of the filesystem holding a path, in bytes.
// Free is the space available to the calling user, which can be less than
// the filesystem's raw free space when blocks are reserved for root.
type Usage struct {
	Total uint64
	Free  uint64
}

package storage

import (
	"path"
	"strings"
)

// PathsOverlap reports whether two storage-relative paths are the same or one
// contains the other, on any adapter. Segments are also compared in their S3
// key form, where "My Job/run" and "My_Job/run" are the same objects, and a
// backslash in an older recorded path counts as the separator S3 makes it.
func PathsOverlap(a, b string) bool {
	as := overlapSegments(a)
	bs := overlapSegments(b)
	for i := 0; i < min(len(as), len(bs)); i++ {
		if SanitizeKeySegment(as[i]) != SanitizeKeySegment(bs[i]) {
			return false
		}
	}
	return true
}

func overlapSegments(p string) []string {
	return strings.Split(path.Clean(strings.ReplaceAll(p, `\`, "/")), "/")
}

//go:build !unix

package engine

import (
	"fmt"
	"os"
)

// openRestoreDestination opens or creates the file at normalizedDst on non-Unix platforms.
func openRestoreDestination(dst, normalizedDst string, perm os.FileMode) (*os.File, error) {
	out, err := os.OpenFile(normalizedDst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|openNoFollow, perm) // #nosec G304 — normalizedDst validated by normalizeRestorePath, restorePathSafe, and openNoFollow
	if err != nil {
		if isSymlinkErr(err) {
			return nil, fmt.Errorf("refusing to copy file through symlink at %s", normalizedDst)
		}
		return nil, fmt.Errorf("creating dest %s: %w", normalizedDst, err)
	}
	return out, nil
}

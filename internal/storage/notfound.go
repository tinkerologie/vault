package storage

import (
	"errors"
	"io/fs"
)

// IsNotExist reports whether err indicates a missing path or object. Adapters
// surface "not found" differently (os.ReadDir returns fs.ErrNotExist; WebDAV
// returns a 404 that List normalises to fs.ErrNotExist), so this gives callers
// a single, adapter-agnostic check. It is used by cleanup code that wants to
// treat an already-deleted directory as success rather than a hard failure.
func IsNotExist(err error) bool {
	return err != nil && errors.Is(err, fs.ErrNotExist)
}

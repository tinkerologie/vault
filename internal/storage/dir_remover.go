package storage

import "errors"

// ErrDirRemovalUnsupported is returned by RemoveEmptyDir on middleware
// wrappers whose underlying provider has no cheap "remove if empty"
// primitive (object stores have no directories; WebDAV's only delete verb
// is recursive). Callers treat it as "stop sweeping", the same way a
// non-empty directory stops the walk.
var ErrDirRemovalUnsupported = errors.New("storage: empty-directory removal not supported by this adapter")

// dirRemover is optionally implemented by providers that can remove an empty
// directory cheaply and safely (the operation fails if the directory is not
// empty). The shared cleanup uses it to sweep directories left empty after the
// last object under them is deleted.
//
// Every middleware wrapper (throttle, retry, metrics, logging) forwards
// RemoveEmptyDir to its inner adapter via forwardRemoveEmptyDir — without
// that, the wrapped chain returned by NewAdapter never satisfied the
// runner's capability assertion and the sweep silently no-oped for every
// destination (issue #168).
type dirRemover interface {
	RemoveEmptyDir(path string) error
}

// forwardRemoveEmptyDir delegates dir removal to inner when it supports the
// capability, and reports ErrDirRemovalUnsupported otherwise. Shared by the
// middleware wrappers so the pass-through stays a one-liner in each.
func forwardRemoveEmptyDir(inner Adapter, dir string) error {
	if dr, ok := inner.(dirRemover); ok {
		return dr.RemoveEmptyDir(dir)
	}
	return ErrDirRemovalUnsupported
}

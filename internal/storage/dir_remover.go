package storage

// dirRemover is optionally implemented by providers that can remove an empty
// directory cheaply and safely (the operation fails if the directory is not
// empty). The shared cleanup uses it to sweep directories left empty after the
// last object under them is deleted. Object stores and WebDAV do not implement
// it: object stores have no directories, and WebDAV's only delete verb is
// recursive, making a safe "remove if empty" impossible.
type dirRemover interface { //nolint:unused
	RemoveEmptyDir(path string) error
}

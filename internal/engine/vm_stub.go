//go:build !linux

package engine

import "fmt"

// VMHandler is a stub for non-Linux platforms where libvirt is not available.
type VMHandler struct{}

// NewVMHandler returns an error on non-Linux platforms.
func NewVMHandler() (*VMHandler, error) {
	return nil, fmt.Errorf("VM backup handler is not supported on this platform (requires Linux)")
}

// ListItems is not supported on this platform.
func (h *VMHandler) ListItems() ([]BackupItem, error) {
	return nil, fmt.Errorf("VM backup handler is not supported on this platform")
}

// Backup is not supported on this platform.
func (h *VMHandler) Backup(item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	return nil, fmt.Errorf("VM backup handler is not supported on this platform")
}

// Restore is not supported on this platform.
func (h *VMHandler) Restore(item BackupItem, sourceDir string, progress ProgressFunc) error {
	return fmt.Errorf("VM backup handler is not supported on this platform")
}

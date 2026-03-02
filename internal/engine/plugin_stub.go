//go:build !linux

package engine

import "fmt"

// PluginHandler is a stub for non-Linux platforms where Unraid plugins
// are not available.
type PluginHandler struct{}

// NewPluginHandler returns an error on non-Linux platforms.
func NewPluginHandler() (*PluginHandler, error) {
	return nil, fmt.Errorf("plugin backup handler is not supported on this platform (requires Linux/Unraid)")
}

// ListItems is not supported on this platform.
func (h *PluginHandler) ListItems() ([]BackupItem, error) {
	return nil, fmt.Errorf("plugin backup handler is not supported on this platform")
}

// Backup is not supported on this platform.
func (h *PluginHandler) Backup(item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	return nil, fmt.Errorf("plugin backup handler is not supported on this platform")
}

// Restore is not supported on this platform.
func (h *PluginHandler) Restore(item BackupItem, sourceDir string, progress ProgressFunc) error {
	return fmt.Errorf("plugin backup handler is not supported on this platform")
}

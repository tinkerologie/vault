//go:build !linux

package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

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
func (h *PluginHandler) Backup(_ context.Context, item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	return nil, fmt.Errorf("plugin backup handler is not supported on this platform")
}

// Restore is not supported on this platform.
func (h *PluginHandler) Restore(_ context.Context, item BackupItem, sourceDir string, progress ProgressFunc) error {
	return fmt.Errorf("plugin backup handler is not supported on this platform")
}

// BackupChunked is not supported on this platform.
func (h *PluginHandler) BackupChunked(_ context.Context, _ BackupItem, _ *dedup.Repo, _ ProgressFunc) (dedup.ID, error) {
	return dedup.ID{}, errors.New("plugin: unsupported on this platform")
}

// RestoreChunked is not supported on this platform.
func (h *PluginHandler) RestoreChunked(_ context.Context, _ BackupItem, _ *dedup.Repo, _ dedup.ID, _ string, _ ProgressFunc) error {
	return errors.New("plugin: unsupported on this platform")
}

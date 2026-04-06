//go:build !linux

package engine

import (
	"context"
	"fmt"
)

// ZFSHandler is a stub for non-Linux platforms where ZFS is not available.
type ZFSHandler struct{}

// NewZFSHandler returns an error on non-Linux platforms.
func NewZFSHandler() (*ZFSHandler, error) {
	return nil, fmt.Errorf("ZFS backup handler is not supported on this platform (requires Linux)")
}

// ListItems is not supported on this platform.
func (h *ZFSHandler) ListItems() ([]BackupItem, error) {
	return nil, fmt.Errorf("ZFS backup handler is not supported on this platform")
}

// Backup is not supported on this platform.
func (h *ZFSHandler) Backup(_ context.Context, _ BackupItem, _ string, _ ProgressFunc) (*BackupResult, error) {
	return nil, fmt.Errorf("ZFS backup handler is not supported on this platform")
}

// Restore is not supported on this platform.
func (h *ZFSHandler) Restore(_ context.Context, _ BackupItem, _ string, _ ProgressFunc) error {
	return fmt.Errorf("ZFS backup handler is not supported on this platform")
}

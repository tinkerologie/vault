//go:build !linux

package engine

import (
	"context"
	"fmt"
)

// ZFSHandler is a stub for non-Linux platforms where ZFS is not available.
type ZFSHandler struct{}

// ZFSPoolInfo describes a ZFS zpool with its root dataset mountpoint.
type ZFSPoolInfo struct {
	Name       string `json:"name"`
	Mountpoint string `json:"mountpoint"`
}

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

// ListNVMePools returns an empty slice on non-Linux platforms.
func (h *ZFSHandler) ListNVMePools() ([]ZFSPoolInfo, error) {
	return []ZFSPoolInfo{}, nil
}

// ListZFSMountpoints returns an empty slice on non-Linux platforms.
func (h *ZFSHandler) ListZFSMountpoints() ([]ZFSPoolInfo, error) {
	return []ZFSPoolInfo{}, nil
}

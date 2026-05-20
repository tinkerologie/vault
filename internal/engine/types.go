package engine

import (
	"context"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

type BackupItem struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"` // "container", "vm", or "folder"
	Settings map[string]any `json:"settings"`
	// Compression controls the archive-level compression applied by the
	// engine when producing tar archives ("none", "gzip", or "zstd"). An
	// empty string is treated as "none". Engines that do not produce tar
	// archives (e.g. VM, ZFS) may ignore this field.
	Compression string `json:"compression,omitempty"`
}

type BackupResult struct {
	ItemName string         `json:"item_name"`
	Success  bool           `json:"success"`
	Error    string         `json:"error"`
	Files    []BackupFile   `json:"files"`
	Meta     map[string]any `json:"meta,omitempty"` // engine-specific metadata (e.g. vm_checkpoint)
}

type BackupFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type ProgressFunc func(item string, percent int, message string)

type Handler interface {
	Backup(ctx context.Context, item BackupItem, dest string, progress ProgressFunc) (*BackupResult, error)
	Restore(ctx context.Context, item BackupItem, source string, progress ProgressFunc) error
	ListItems() ([]BackupItem, error)
}

// ChunkedHandler is the optional sibling of Handler for handlers that can
// produce content-defined dedup output (folder, plugin, container — see
// the dedup design spec). Runner branches on dest.DedupEnabled at backup
// time and on restore_point.manifest_id at restore time; handlers that
// don't satisfy this interface continue to use the classic tar path on
// dedup destinations.
type ChunkedHandler interface {
	BackupChunked(ctx context.Context, item BackupItem, repo *dedup.Repo, progress ProgressFunc) (dedup.ID, error)
	RestoreChunked(ctx context.Context, item BackupItem, repo *dedup.Repo, manifestID dedup.ID, destPath string, progress ProgressFunc) error
}

//go:build !linux

package mount

import (
	"context"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// MountManifests returns ErrFUSENotSupported on non-Linux platforms.
func MountManifests(ctx context.Context, repo *dedup.Repo, manifests map[string]dedup.Manifest, mountPath string, onActivity func()) (MountHandle, error) {
	return nil, ErrFUSENotSupported
}

func unmountPlatform(path string) error {
	return nil
}

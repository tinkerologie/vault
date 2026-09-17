package mount

import (
	"errors"
)

// ErrFUSENotSupported is returned when attempting to mount FUSE on non-Linux platforms.
var ErrFUSENotSupported = errors.New("fuse: read-only mounts are only supported on Linux")

// MountHandle controls an active FUSE mount.
type MountHandle interface {
	Unmount() error
	MountPath() string
}

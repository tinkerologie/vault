package engine

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	mounttypes "github.com/moby/moby/api/types/mount"

	"github.com/tinkerologie/vault/internal/safepath"
)

// structuredRestoreBinds maps Docker's --mount bind syntax using the same
// source as volume extraction. The other structured mount types are unchanged.
func (inspect restoreInspect) structuredRestoreBinds(destination string) ([]mounttypes.Mount, []volumePathRewrite, error) {
	if len(inspect.HostConfig.Mounts) == 0 {
		return nil, nil, nil
	}
	effective := map[string]mountInfo{}
	for _, saved := range inspect.mountInfos() {
		if _, repeated := effective[saved.Destination]; repeated {
			effective[saved.Destination] = mountInfo{}
			continue
		}
		effective[saved.Destination] = saved
	}
	targets := map[string]bool{}
	for _, bind := range inspect.HostConfig.Binds {
		parts := strings.SplitN(bind, ":", 3)
		if len(parts) > 1 {
			targets[parts[1]] = true
		}
	}
	var mounts []mounttypes.Mount
	var rewrites []volumePathRewrite
	for _, mount := range inspect.HostConfig.Mounts {
		if mount.Type != mounttypes.TypeBind {
			continue
		}
		saved := effective[mount.Target]
		if targets[mount.Target] || !path.IsAbs(mount.Target) || path.Clean(mount.Target) != mount.Target ||
			saved.Type != string(mounttypes.TypeBind) || saved.Source != mount.Source || !filepath.IsAbs(mount.Source) {
			return nil, nil, errors.New("structured bind mount does not match the saved container mounts")
		}
		source, err := volumeRestoreTarget(destination, saved.Type, saved.Name, saved.Source)
		if err != nil {
			return nil, nil, err
		}
		if _, err := safepath.CheckVolumeTarget(source); err != nil {
			return nil, nil, fmt.Errorf("structured bind source: %w", err)
		}
		source, err = normalizeRestorePath(source)
		if err != nil {
			return nil, nil, err
		}
		if destination != "" {
			relative, err := filepath.Rel(destination, source)
			if err != nil || relative == "." || !filepath.IsLocal(relative) {
				return nil, nil, errors.New("structured bind source resolves outside the alternate destination")
			}
			rewrites = append(rewrites, volumePathRewrite{Old: mount.Source, New: source})
		}
		mount.Source = source
		targets[mount.Target] = true
		mounts = append(mounts, mount)
	}
	return mounts, rewrites, nil
}

func structuredBindSource(binds []mounttypes.Mount, target string) string {
	for _, bind := range binds {
		if bind.Target == target {
			return bind.Source
		}
	}
	return ""
}

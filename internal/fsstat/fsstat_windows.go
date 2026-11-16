//go:build windows

package fsstat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Stat returns the capacity of the volume that holds path.
func Stat(path string) (Usage, error) {
	dir, err := volumeDir(path)
	if err != nil {
		return Usage{}, fmt.Errorf("disk free space %s: %w", path, err)
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return Usage{}, fmt.Errorf("disk free space %s: %w", path, err)
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &totalFree); err != nil {
		return Usage{}, fmt.Errorf("disk free space %s: %w", path, err)
	}
	return Usage{Total: total, Free: freeToCaller}, nil
}

// SameFilesystem compares the Windows volume mount paths of two local paths.
func SameFilesystem(first, second string) (bool, error) {
	a, err := volumeMountPath(first)
	if err != nil {
		return false, err
	}
	b, err := volumeMountPath(second)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(a, b), nil
}

func volumeMountPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return "", err
	}
	var mount [windows.MAX_PATH]uint16
	if err := windows.GetVolumePathName(p, &mount[0], uint32(len(mount))); err != nil {
		return "", fmt.Errorf("get volume path %s: %w", path, err)
	}
	return windows.UTF16ToString(mount[:]), nil
}

// volumeDir turns path into the form GetDiskFreeSpaceEx accepts: an existing
// file is replaced by its folder (the API takes directories only, while
// statfs on Unix also accepts files), and the folder ends in a separator,
// which the API requires for a UNC root such as \\server\share.
func volumeDir(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		path = filepath.Dir(path)
	}
	return withTrailingSeparator(path), nil
}

func withTrailingSeparator(path string) string {
	if strings.HasSuffix(path, `\`) || strings.HasSuffix(path, "/") {
		return path
	}
	return path + `\`
}

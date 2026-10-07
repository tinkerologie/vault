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

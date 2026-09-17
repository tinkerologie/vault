//go:build unix

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ruaan-deysel/vault/internal/safepath"
	"golang.org/x/sys/unix"
)

// openRestoreDestination opens or creates the file at dst/normalizedDst using descriptor-relative
// traversal beneath the validated restore root to make directory traversal atomic and prevent
// intermediate-parent symlink swaps.
func openRestoreDestination(dst, normalizedDst string, perm os.FileMode) (*os.File, error) {
	// Identify the matching approved root
	var matchingRoot string
	for _, root := range safepath.RestoreAllowedRoots() {
		cleanRoot := filepath.Clean(root)
		if dst == cleanRoot || strings.HasPrefix(dst, cleanRoot+string(filepath.Separator)) {
			matchingRoot = cleanRoot
			break
		}
	}
	if matchingRoot == "" {
		for _, root := range safepath.RestoreAllowedRoots() {
			cleanRoot := filepath.Clean(root)
			if normalizedDst == cleanRoot || strings.HasPrefix(normalizedDst, cleanRoot+string(filepath.Separator)) {
				matchingRoot = cleanRoot
				break
			}
		}
	}
	if matchingRoot == "" {
		return nil, fmt.Errorf("restore destination %s is not under an approved root", normalizedDst)
	}

	targetPath := dst
	rel, err := filepath.Rel(matchingRoot, targetPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "..\\") {
		targetPath = normalizedDst
		rel, err = filepath.Rel(matchingRoot, targetPath)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "..\\") {
			return nil, fmt.Errorf("invalid relative restore path %q", normalizedDst)
		}
	}

	// Open the approved root directory with O_DIRECTORY|O_CLOEXEC
	rootFd, err := unix.Open(matchingRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening restore root %s: %w", matchingRoot, err)
	}
	currentFd := rootFd
	defer func() {
		if currentFd >= 0 {
			_ = unix.Close(currentFd)
		}
	}()

	parts := strings.Split(filepath.ToSlash(rel), "/")

	// Walk parent directories using descriptor-relative open with O_NOFOLLOW
	for i := 0; i < len(parts)-1; i++ {
		comp := parts[i]
		if comp == "" || comp == "." {
			continue
		}
		if comp == ".." {
			return nil, fmt.Errorf("traversal not allowed in restore path")
		}

		nextFd, err := unix.Openat(currentFd, comp, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			if isSymlinkErr(err) {
				return nil, fmt.Errorf("refusing to traverse symlinked directory at %s in %s", comp, targetPath)
			}
			return nil, fmt.Errorf("traversing %s: %w", comp, err)
		}
		_ = unix.Close(currentFd)
		currentFd = nextFd
	}

	// Open/create the final file relative to the retained parent descriptor
	leaf := parts[len(parts)-1]
	flags := unix.O_CREAT | unix.O_WRONLY | unix.O_TRUNC | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fileFd, err := unix.Openat(currentFd, leaf, flags, uint32(perm.Perm()))
	if err != nil {
		if isSymlinkErr(err) {
			return nil, fmt.Errorf("refusing to copy file through symlink at %s", targetPath)
		}
		return nil, fmt.Errorf("creating dest %s: %w", targetPath, err)
	}

	return os.NewFile(uintptr(fileFd), targetPath), nil
}

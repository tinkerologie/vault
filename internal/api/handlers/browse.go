package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BrowseHandler serves filesystem directory listings for the path browser UI.
type BrowseHandler struct{}

// NewBrowseHandler creates a new BrowseHandler.
func NewBrowseHandler() *BrowseHandler {
	return &BrowseHandler{}
}

// dirEntry represents a single directory in the browse response.
type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// unraidRoots are the well-known Unraid mount points shown as top-level shortcuts.
var unraidRoots = []dirEntry{
	{Name: "Flash Drive", Path: "/boot"},
	{Name: "User Shares", Path: "/mnt/user"},
	{Name: "Cache", Path: "/mnt/cache"},
	{Name: "Unassigned Devices", Path: "/mnt/disks"},
	{Name: "Remote Mounts", Path: "/mnt/remotes"},
}

// allowedPrefixes are the filesystem prefixes allowed for browsing.
var allowedPrefixes = []string{"/mnt", "/boot"}

// List returns subdirectories of a given path. Only paths under /mnt/ are allowed.
// When no path query param is provided, it returns Unraid well-known roots.
//
//	GET /api/v1/browse?path=/mnt/user
func (h *BrowseHandler) List(w http.ResponseWriter, r *http.Request) {
	qpath := r.URL.Query().Get("path")

	// No path — return well-known Unraid roots plus any array disks found.
	if qpath == "" {
		roots := h.discoverRoots()
		respondJSON(w, http.StatusOK, map[string]any{
			"path":    "/mnt",
			"entries": roots,
		})
		return
	}

	// Security: only allow browsing under allowed prefixes.
	clean := filepath.Clean(qpath)
	allowed := false
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(clean, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		respondError(w, http.StatusForbidden, "browsing is restricted to /mnt/ and /boot/")
		return
	}

	entries, err := h.listDirs(clean)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"path":    clean,
		"entries": entries,
	})
}

// discoverRoots returns Unraid well-known roots plus dynamically discovered
// array disks (/mnt/disk1, /mnt/disk2, etc.) and cache pools.
func (h *BrowseHandler) discoverRoots() []dirEntry {
	roots := make([]dirEntry, 0, len(unraidRoots)+8)

	// Add well-known roots that actually exist on this system.
	for _, r := range unraidRoots {
		if info, err := os.Stat(r.Path); err == nil && info.IsDir() {
			roots = append(roots, r)
		}
	}

	// Discover array disks: /mnt/disk1, /mnt/disk2, ...
	mntEntries, err := os.ReadDir("/mnt")
	if err != nil {
		return roots
	}
	for _, e := range mntEntries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Match disk1..disk99 (array disks).
		if strings.HasPrefix(name, "disk") && len(name) > 4 {
			suffix := name[4:]
			isArrayDisk := true
			for _, ch := range suffix {
				if ch < '0' || ch > '9' {
					isArrayDisk = false
					break
				}
			}
			if isArrayDisk {
				roots = append(roots, dirEntry{
					Name: "Array Disk " + suffix,
					Path: "/mnt/" + name,
				})
			}
		}
		// Match additional cache pools (cache2, cache3, etc.).
		if strings.HasPrefix(name, "cache") && name != "cache" {
			roots = append(roots, dirEntry{
				Name: "Cache Pool (" + name + ")",
				Path: "/mnt/" + name,
			})
		}
	}

	return roots
}

// listDirs reads directory entries and returns only subdirectories.
func (h *BrowseHandler) listDirs(path string) ([]dirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	dirs := make([]dirEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Skip hidden directories.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dirs = append(dirs, dirEntry{
			Name: e.Name(),
			Path: filepath.Join(path, e.Name()),
		})
	}

	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].Name < dirs[j].Name
	})

	return dirs, nil
}

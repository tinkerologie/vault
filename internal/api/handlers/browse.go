package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ruaan-deysel/vault/internal/safepath"
	"github.com/ruaan-deysel/vault/internal/unraid"
)

// BrowseHandler serves filesystem directory listings for the path browser UI.
type BrowseHandler struct{}

// NewBrowseHandler creates a new BrowseHandler.
func NewBrowseHandler() *BrowseHandler {
	return &BrowseHandler{}
}

// dirEntry represents a single directory in the browse response.
type dirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// unraidRoots are the well-known Unraid mount points shown as top-level shortcuts.
// Note: the "Cache" entry is intentionally omitted here — cache pools (including
// mirrored and custom-named pools) are discovered dynamically below.
var unraidRoots = []dirEntry{
	{Name: "Flash Drive", Path: "/boot", IsDir: true},
	{Name: "User Shares", Path: "/mnt/user", IsDir: true},
	{Name: "Unassigned Devices", Path: "/mnt/disks", IsDir: true},
	{Name: "Remote Mounts", Path: "/mnt/remotes", IsDir: true},
}

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

	normalizedPath, err := normalizeBrowsePath(qpath)
	if err != nil {
		respondError(w, http.StatusForbidden, "browsing is restricted to /mnt/ and /boot/")
		return
	}

	entries, err := h.listEntries(normalizedPath, r.URL.Query().Get("files") == "true")
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"path":    normalizedPath,
		"entries": entries,
	})
}

// discoverRoots returns Unraid well-known roots plus dynamically discovered
// array disks (/mnt/disk1, /mnt/disk2, etc.) and cache/pool drives.
func (h *BrowseHandler) discoverRoots() []dirEntry {
	roots := make([]dirEntry, 0, len(unraidRoots)+8)

	// Add well-known roots that actually exist on this system.
	for _, r := range unraidRoots {
		if info, err := os.Stat(r.Path); err == nil && info.IsDir() {
			roots = append(roots, r)
		}
	}

	// Discover pool drives via the shared utility.
	pools := unraid.DiscoverPools()
	for _, poolPath := range pools {
		name := filepath.Base(poolPath)
		label := "Cache"
		if name != "cache" {
			label = "Cache Pool (" + name + ")"
		}
		if info, err := os.Stat(poolPath); err == nil && info.IsDir() {
			roots = append(roots, dirEntry{
				Name:  label,
				Path:  poolPath,
				IsDir: true,
			})
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
					Name:  "Array Disk " + suffix,
					Path:  "/mnt/" + name,
					IsDir: true,
				})
			}
		}
	}

	return roots
}

// listEntries reads directory entries. When includeFiles is false, only
// subdirectories are returned (the default). When true, files are included
// alongside directories so the browser can be used to pick individual files.
func (h *BrowseHandler) listEntries(path string, includeFiles bool) ([]dirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	result := make([]dirEntry, 0, len(entries))
	for _, e := range entries {
		// Skip hidden entries.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		entryPath, err := safepath.JoinUnderBase(path, e.Name(), false)
		if err != nil {
			continue
		}
		if e.IsDir() {
			result = append(result, dirEntry{
				Name:  e.Name(),
				Path:  entryPath,
				IsDir: true,
			})
		} else if includeFiles {
			result = append(result, dirEntry{
				Name:  e.Name(),
				Path:  entryPath,
				IsDir: false,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		// Directories first, then alphabetical.
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
}

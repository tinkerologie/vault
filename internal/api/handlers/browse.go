package handlers

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ruaan-deysel/vault/internal/safepath"
	"github.com/ruaan-deysel/vault/internal/unraid"
)

// BrowseHandler serves filesystem directory listings for the path browser UI.
type BrowseHandler struct {
	zfsLister         ZFSMountpointLister
	extraAllowedRoots []string
}

// ZFSMountpointLister enumerates ZFS dataset mountpoints for browse discovery.
type ZFSMountpointLister interface {
	ListZFSMountpoints() ([]ZFSMountInfo, error)
}

// ZFSMountInfo describes a ZFS pool mountpoint for browse discovery.
type ZFSMountInfo struct {
	Name       string
	Mountpoint string
}

// NewBrowseHandler creates a new BrowseHandler.
func NewBrowseHandler() *BrowseHandler {
	return &BrowseHandler{}
}

// SetZFSLister sets the ZFS mountpoint lister for ZFS-aware browsing.
// It also pre-populates extra allowed roots from ZFS mountpoints so path
// validation accepts ZFS locations that may fall outside /mnt and /boot.
// The lister is only stored when mountpoints are successfully fetched so
// that discoverRoots and normalizePath stay consistent.
func (h *BrowseHandler) SetZFSLister(lister ZFSMountpointLister) {
	if lister == nil {
		return
	}

	mounts, err := lister.ListZFSMountpoints()
	if err != nil {
		log.Printf("browse: failed to list ZFS mountpoints: %v", err)
		return
	}

	// Lister is only assigned after a successful fetch so discoverRoots
	// and normalizePath stay in sync with extraAllowedRoots.
	h.zfsLister = lister

	h.mergeExtraRoots(mounts)
}

// mergeExtraRoots adds cleaned, deduplicated ZFS mountpoints to
// extraAllowedRoots. It skips empty, non-absolute, and root ("/") paths.
func (h *BrowseHandler) mergeExtraRoots(mounts []ZFSMountInfo) {
	seen := make(map[string]struct{}, len(h.extraAllowedRoots))
	for _, root := range h.extraAllowedRoots {
		seen[filepath.Clean(root)] = struct{}{}
	}

	for _, m := range mounts {
		mp := filepath.Clean(m.Mountpoint)
		if mp == "" || !filepath.IsAbs(mp) || mp == "/" {
			continue
		}
		if _, ok := seen[mp]; ok {
			continue
		}
		h.extraAllowedRoots = append(h.extraAllowedRoots, mp)
		seen[mp] = struct{}{}
	}
}

// normalizePath validates a browse path against the static allowed roots
// plus any ZFS mountpoints discovered at startup. ZFS roots are always
// included in path validation (not gated by include_zfs) because they are
// legitimate filesystem locations used for database and staging configuration.
// The include_zfs query parameter only controls whether ZFS roots appear in the
// top-level root listing.
func (h *BrowseHandler) normalizePath(path string) (string, error) {
	roots := browseAllowedRoots
	if len(h.extraAllowedRoots) > 0 {
		combined := make([]string, 0, len(browseAllowedRoots)+len(h.extraAllowedRoots))
		combined = append(combined, browseAllowedRoots...)
		combined = append(combined, h.extraAllowedRoots...)
		roots = combined
	}
	return safepath.NormalizeAbsoluteUnderRoots(path, roots)
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

// List returns subdirectories of a given path. Paths must be under allowed roots
// (/mnt, /boot, or discovered ZFS mountpoints). When no path query param is
// provided, it returns Unraid well-known roots plus optional ZFS pools.
//
//	GET /api/v1/browse?path=/mnt/user
func (h *BrowseHandler) List(w http.ResponseWriter, r *http.Request) {
	qpath := r.URL.Query().Get("path")

	// No path — return well-known Unraid roots plus any array disks found.
	if qpath == "" {
		includeZFS := r.URL.Query().Get("include_zfs") == "true"
		roots := h.discoverRoots(includeZFS)
		respondJSON(w, http.StatusOK, map[string]any{
			"path":    "/mnt",
			"entries": roots,
		})
		return
	}

	normalizedPath, err := h.normalizePath(qpath)
	if err != nil {
		respondError(w, http.StatusForbidden, "path is outside allowed roots")
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
// When includeZFS is true, ZFS dataset mountpoints are also included.
func (h *BrowseHandler) discoverRoots(includeZFS bool) []dirEntry {
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

	// Append ZFS pool mountpoints when requested.
	if includeZFS && h.zfsLister != nil {
		seen := make(map[string]bool, len(roots))
		for _, r := range roots {
			seen[r.Path] = true
		}
		zfsMounts, zfsErr := h.zfsLister.ListZFSMountpoints()
		if zfsErr == nil {
			for _, m := range zfsMounts {
				if seen[m.Mountpoint] {
					continue
				}
				roots = append(roots, dirEntry{
					Name:  "ZFS Pool: " + m.Name,
					Path:  m.Mountpoint,
					IsDir: true,
				})
				seen[m.Mountpoint] = true
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

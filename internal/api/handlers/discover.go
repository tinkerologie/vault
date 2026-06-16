package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ruaan-deysel/vault/internal/engine"
)

// DiscoverHandler exposes container and VM discovery via the engine.
type DiscoverHandler struct{}

// NewDiscoverHandler creates a new DiscoverHandler.
func NewDiscoverHandler() *DiscoverHandler {
	return &DiscoverHandler{}
}

// ListContainers returns all Docker containers discoverable by the engine.
//
//	GET /api/v1/containers
func (h *DiscoverHandler) ListContainers(w http.ResponseWriter, r *http.Request) {
	handler, err := engine.NewContainerHandler()
	if err != nil {
		// Docker not available — return empty list, not an error.
		respondJSON(w, http.StatusOK, map[string]any{
			"items":     []engine.BackupItem{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	items, err := handler.ListItems()
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"items":     []engine.BackupItem{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"available": true,
	})
}

// ContainerMounts returns the bind mounts of a single container, each annotated
// with the auto-skip verdict from the backup engine so the job wizard can render
// per-mount include/exclude toggles.
//
//	GET /api/v1/containers/{name}/mounts
func (h *DiscoverHandler) ContainerMounts(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	handler, err := engine.NewContainerHandler()
	if err != nil {
		// Docker not available — return empty list, not an error.
		respondJSON(w, http.StatusOK, map[string]any{
			"mounts":    []engine.MountInfo{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	mounts, err := handler.ListMounts(r.Context(), name)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"mounts":    []engine.MountInfo{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"mounts":    mounts,
		"available": true,
	})
}

// ListVMs returns all libvirt VMs discoverable by the engine.
//
//	GET /api/v1/vms
func (h *DiscoverHandler) ListVMs(w http.ResponseWriter, r *http.Request) {
	handler, err := engine.NewVMHandler() //nolint:staticcheck // platform-dependent: stub always returns error on non-Linux
	if err != nil {                       //nolint:staticcheck // platform-dependent: stub always returns error on non-Linux
		// libvirt not available — return empty list gracefully.
		respondJSON(w, http.StatusOK, map[string]any{
			"items":     []engine.BackupItem{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	items, err := handler.ListItems() //nolint:staticcheck // platform-dependent
	if err != nil {                   //nolint:staticcheck // platform-dependent: stub always returns error on non-Linux
		respondJSON(w, http.StatusOK, map[string]any{
			"items":     []engine.BackupItem{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"available": true,
	})
}

// ListFolders returns folder presets (e.g. Flash Drive) discoverable by the engine.
//
//	GET /api/v1/folders
func (h *DiscoverHandler) ListFolders(w http.ResponseWriter, r *http.Request) {
	handler, err := engine.NewFolderHandler()
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"items":     []engine.BackupItem{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	items, err := handler.ListItems()
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"items":     []engine.BackupItem{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"available": true,
	})
}

// ListPlugins returns installed Unraid plugins discoverable by the engine.
//
//	GET /api/v1/plugins
func (h *DiscoverHandler) ListPlugins(w http.ResponseWriter, r *http.Request) {
	handler, err := engine.NewPluginHandler() //nolint:staticcheck // platform-dependent: stub always returns error on non-Linux
	if err != nil {                           //nolint:staticcheck // platform-dependent: stub always returns error on non-Linux
		// Plugin handler not available (non-Linux) — return empty list gracefully.
		respondJSON(w, http.StatusOK, map[string]any{
			"items":     []engine.BackupItem{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	items, err := handler.ListItems() //nolint:staticcheck // platform-dependent
	if err != nil {                   //nolint:staticcheck // platform-dependent: stub always returns error on non-Linux
		respondJSON(w, http.StatusOK, map[string]any{
			"items":     []engine.BackupItem{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"available": true,
	})
}

// ListZFSDatasets returns all ZFS datasets discoverable by the engine.
//
//	GET /api/v1/zfs
func (h *DiscoverHandler) ListZFSDatasets(w http.ResponseWriter, r *http.Request) {
	handler, err := engine.NewZFSHandler() //nolint:staticcheck // platform-dependent: stub always returns error on non-Linux
	if err != nil {                        //nolint:staticcheck // platform-dependent: stub always returns error on non-Linux
		// ZFS not available — return empty list gracefully.
		respondJSON(w, http.StatusOK, map[string]any{
			"items":     []engine.BackupItem{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	items, err := handler.ListItems() //nolint:staticcheck // platform-dependent
	if err != nil {                   //nolint:staticcheck // platform-dependent: stub always returns error on non-Linux
		respondJSON(w, http.StatusOK, map[string]any{
			"items":     []engine.BackupItem{},
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"available": true,
	})
}

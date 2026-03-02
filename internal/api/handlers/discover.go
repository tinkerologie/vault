package handlers

import (
	"net/http"

	"github.com/ruaandeysel/vault/internal/engine"
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

// ListVMs returns all libvirt VMs discoverable by the engine.
//
//	GET /api/v1/vms
func (h *DiscoverHandler) ListVMs(w http.ResponseWriter, r *http.Request) {
	handler, err := engine.NewVMHandler()
	if err != nil {
		// libvirt not available — return empty list gracefully.
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
	handler, err := engine.NewPluginHandler()
	if err != nil {
		// Plugin handler not available (non-Linux) — return empty list gracefully.
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

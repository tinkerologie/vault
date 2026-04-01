package handlers

import (
	"net/http"

	"github.com/ruaan-deysel/vault/internal/config"
)

// PresetsHandler serves exclusion preset data.
type PresetsHandler struct{}

// NewPresetsHandler creates a new PresetsHandler.
func NewPresetsHandler() *PresetsHandler {
	return &PresetsHandler{}
}

// GetExclusions returns recommended exclusion paths for a container image.
//
//	GET /api/v1/presets/exclusions?image=<image_name>
func (h *PresetsHandler) GetExclusions(w http.ResponseWriter, r *http.Request) {
	image := r.URL.Query().Get("image")
	paths := config.GetExclusionPreset(image)
	if paths == nil {
		paths = []string{}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"paths": paths,
		"image": image,
	})
}

package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/ruaan-deysel/vault/internal/config"
	"github.com/ruaan-deysel/vault/internal/engine"
)

// socketDetectTimeout caps Docker inspect calls used to auto-suggest socket
// exclusions. Kept short because the response is interactive (job editor).
const socketDetectTimeout = 3 * time.Second

// PresetsHandler serves exclusion preset data.
type PresetsHandler struct{}

// NewPresetsHandler creates a new PresetsHandler.
func NewPresetsHandler() *PresetsHandler {
	return &PresetsHandler{}
}

// GetExclusions returns recommended exclusion paths for a container.
//
// The response merges two sources:
//
//   - Image-based presets keyed off the `image` query parameter (static map in
//     internal/config/presets.go).
//
//   - Live socket bind-mount detection when the `container` query parameter is
//     supplied. Any mount whose host source ends in ".sock" (e.g.
//     /var/run/docker.sock, /var/run/docker-shim.sock) is auto-suggested
//     because Go's archive/tar cannot serialize socket inodes.
//
//     GET /api/v1/presets/exclusions?image=<image_name>&container=<name_or_id>
func (h *PresetsHandler) GetExclusions(w http.ResponseWriter, r *http.Request) {
	image := r.URL.Query().Get("image")
	containerName := r.URL.Query().Get("container")

	seen := make(map[string]struct{})
	paths := make([]string, 0)
	add := func(p string) {
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}

	for _, p := range config.GetExclusionPreset(image) {
		add(p)
	}

	if containerName != "" {
		if ch, err := engine.NewContainerHandler(); err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), socketDetectTimeout)
			defer cancel()
			if sockets, derr := ch.DetectSocketMounts(ctx, containerName); derr == nil {
				for _, p := range sockets {
					add(p)
				}
			}
		}
	}

	resp := map[string]any{
		"paths":     paths,
		"image":     image,
		"container": containerName,
	}

	// Merge advisory metadata (notes/warnings) when the image has any, e.g. the
	// Immich database caveat. Absent for most containers, so the keys only
	// appear when relevant.
	if meta, ok := config.GetPresetMeta(image); ok {
		if len(meta.Notes) > 0 {
			resp["notes"] = meta.Notes
		}
		if len(meta.Warnings) > 0 {
			resp["warnings"] = meta.Warnings
		}
	}

	respondJSON(w, http.StatusOK, resp)
}

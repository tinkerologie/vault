package handlers

import (
	"fmt"
	"net/http"

	"github.com/tinkerologie/vault/internal/diagnostics"
	"github.com/tinkerologie/vault/internal/memlimit"
	"github.com/tinkerologie/vault/internal/storage"
)

// UploadBudgetWarning flags an S3 job whose multipart upload buffers can
// exceed the memory limit (issue #500). The estimate covers that job's
// uploads only; verification, replication, mounts and dedup packing can
// overlap with it.
type UploadBudgetWarning struct {
	JobID         int64  `json:"job_id"`
	JobName       string `json:"job_name"`
	Destination   string `json:"destination"`
	EstimateBytes int64  `json:"estimate_bytes"`
	LimitBytes    int64  `json:"limit_bytes"`
}

// RuntimeResponse is the body of GET /api/v1/settings/runtime.
type RuntimeResponse struct {
	Runtime          diagnostics.RuntimeInfo `json:"runtime"`
	MemoryLimit      memlimit.State          `json:"memory_limit"`
	SuggestedLimitMB int64                   `json:"suggested_limit_mb"`
	UploadWarnings   []UploadBudgetWarning   `json:"upload_warnings"`
}

// GetRuntime returns live heap, GC and RSS statistics, the effective memory
// limit and any S3 upload budget warnings.
//
//	GET /api/v1/settings/runtime
func (h *SettingsHandler) GetRuntime(w http.ResponseWriter, _ *http.Request) {
	if h.diagnosticsCollector == nil {
		respondError(w, http.StatusServiceUnavailable, "diagnostics not configured")
		return
	}
	rt := h.diagnosticsCollector.Runtime()
	lim := memlimit.Current()
	warnings, err := h.uploadBudgetWarnings(lim.LimitBytes)
	if err != nil {
		respondInternalError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, RuntimeResponse{
		Runtime:          rt,
		MemoryLimit:      lim,
		SuggestedLimitMB: memlimit.SuggestedMB(rt.HostMemTotalBytes),
		UploadWarnings:   warnings,
	})
}

// uploadBudgetWarnings lists enabled jobs on S3 destinations whose upload
// buffer estimate exceeds limit. No limit means no warnings.
func (h *SettingsHandler) uploadBudgetWarnings(limit int64) ([]UploadBudgetWarning, error) {
	out := []UploadBudgetWarning{}
	if limit <= 0 {
		return out, nil
	}
	dests, err := h.db.ListStorageDestinations()
	if err != nil {
		return nil, fmt.Errorf("listing storage destinations: %w", err)
	}
	s3Dests := make(map[int64]int, len(dests))
	for i, d := range dests {
		if d.Type == "s3" {
			s3Dests[d.ID] = i
		}
	}
	if len(s3Dests) == 0 {
		return out, nil
	}
	jobs, err := h.db.ListJobs()
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	for _, j := range jobs {
		i, ok := s3Dests[j.StorageDestID]
		if !ok || !j.Enabled {
			continue
		}
		est, err := storage.S3UploadBufferBytes(dests[i].Config, j.EffectiveUploadConcurrency())
		if err != nil || est <= limit {
			continue
		}
		out = append(out, UploadBudgetWarning{
			JobID:         j.ID,
			JobName:       j.Name,
			Destination:   dests[i].Name,
			EstimateBytes: est,
			LimitBytes:    limit,
		})
	}
	return out, nil
}

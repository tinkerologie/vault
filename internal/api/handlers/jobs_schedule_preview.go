package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/tinkerologie/vault/internal/scheduler"
)

// maxSchedulePreviewLen bounds the schedule query parameter; a real cron
// expression is a few dozen characters.
const maxSchedulePreviewLen = 256

// SchedulePreviewResponse lists a schedule's upcoming runs on the server's
// clock, which is the clock the scheduler fires on.
type SchedulePreviewResponse struct {
	Runs []string `json:"runs"`
	// Timezone is the server's zone abbreviation (e.g. "CET") and UTCOffset its
	// current offset ("+01:00"), so the UI can label the times honestly.
	Timezone  string `json:"timezone"`
	UTCOffset string `json:"utc_offset"`
}

// SchedulePreview returns the next runs of a cron expression in the server's
// timezone. It is read-only and does not touch any job.
//
//	GET /api/v1/jobs/schedule-preview?schedule=<cron>&count=<n>
func (h *JobHandler) SchedulePreview(w http.ResponseWriter, r *http.Request) {
	spec := r.URL.Query().Get("schedule")
	if len(spec) > maxSchedulePreviewLen {
		respondError(w, http.StatusBadRequest, "schedule is too long")
		return
	}
	count := 3
	if raw := r.URL.Query().Get("count"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			respondError(w, http.StatusBadRequest, "count must be a positive integer")
			return
		}
		count = n
	}
	now := time.Now()
	runs, err := scheduler.PreviewRuns(spec, now, count)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid schedule: "+err.Error())
		return
	}
	out := make([]string, 0, len(runs))
	for _, t := range runs {
		out = append(out, t.Format(time.RFC3339))
	}
	zone, _ := now.Zone()
	respondJSON(w, http.StatusOK, SchedulePreviewResponse{
		Runs:      out,
		Timezone:  zone,
		UTCOffset: now.Format("-07:00"),
	})
}

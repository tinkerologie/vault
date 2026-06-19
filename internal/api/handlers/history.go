package handlers

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
)

// HistoryHandler serves job run history endpoints.
type HistoryHandler struct {
	db *db.DB
}

// NewHistoryHandler creates a new HistoryHandler.
func NewHistoryHandler(database *db.DB) *HistoryHandler {
	return &HistoryHandler{db: database}
}

// Purge deletes all job run history records.
//
//	DELETE /api/v1/history
func (h *HistoryHandler) Purge(w http.ResponseWriter, _ *http.Request) {
	count, err := h.db.PurgeJobRuns()
	if err != nil {
		log.Printf("ERROR purging job runs: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	h.db.LogActivity("info", "system", "Job run history purged", fmt.Sprintf(`{"deleted_count":%d}`, count))
	w.WriteHeader(http.StatusNoContent)
}

// trendRun is one completed run reduced to what the trend needs.
type trendRun struct {
	Start    time.Time
	Size     int64
	Category string
}

// trendBucket is one chart bar.
type trendBucket struct {
	Start      time.Time        `json:"start"`
	TotalBytes int64            `json:"total_bytes"`
	Categories map[string]int64 `json:"categories"`
}

// periodToWindow maps a period to (lookback duration, bucket granularity).
func periodToWindow(period string) (time.Duration, string, bool) {
	switch period {
	case "7d":
		return 7 * 24 * time.Hour, "run", true
	case "30d":
		return 30 * 24 * time.Hour, "day", true
	case "90d":
		return 90 * 24 * time.Hour, "day", true
	case "6m":
		return 182 * 24 * time.Hour, "week", true
	case "1y":
		return 365 * 24 * time.Hour, "week", true
	default:
		return 0, "", false
	}
}

// bucketKey returns the UTC bucket-start for a run time at the given granularity.
func bucketKey(t time.Time, bucket string) time.Time {
	u := t.UTC()
	switch bucket {
	case "day":
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	case "week":
		offset := (int(u.Weekday()) + 6) % 7 // snap back to Monday
		d := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
		return d.AddDate(0, 0, -offset)
	default: // "run"
		return u
	}
}

// bucketTrend groups runs (any order) into ordered buckets summed by category.
func bucketTrend(runs []trendRun, bucket string) []trendBucket {
	out := []trendBucket{}
	if len(runs) == 0 {
		return out
	}
	index := map[int64]int{} // bucket-start unix -> position in out
	for _, r := range runs {
		key := bucketKey(r.Start, bucket)
		k := key.Unix()
		pos, ok := index[k]
		if !ok {
			pos = len(out)
			index[k] = pos
			out = append(out, trendBucket{Start: key, Categories: map[string]int64{}})
		}
		out[pos].TotalBytes += r.Size
		cat := r.Category
		if cat == "" {
			cat = "other"
		}
		out[pos].Categories[cat] += r.Size
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// Trend serves GET /api/v1/history/trend?period=7d|30d|90d|6m|1y
func (h *HistoryHandler) Trend(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "30d"
	}
	window, bucket, ok := periodToWindow(period)
	if !ok {
		respondError(w, http.StatusBadRequest, "invalid period (use 7d|30d|90d|6m|1y)")
		return
	}
	since := time.Now().Add(-window)

	jobs, err := h.db.ListJobs()
	if err != nil {
		respondInternalError(w, err)
		return
	}
	cat := map[int64]string{}
	for _, j := range jobs {
		items, err := h.db.GetJobItems(j.ID)
		if err != nil {
			log.Printf("trend: listing items for job %d: %v", j.ID, err)
		}
		cat[j.ID] = dominantCategory(items)
	}

	var runs []trendRun
	for _, j := range jobs {
		// Time-filtered in SQL (and without the log payload) so we don't load
		// the full run history into memory just to discard most of it.
		jrs, err := h.db.GetJobRunsSince(j.ID, since)
		if err != nil {
			log.Printf("trend: listing runs for job %d: %v", j.ID, err)
			continue
		}
		for _, run := range jrs {
			if run.SizeBytes <= 0 {
				continue
			}
			if run.Status != "success" && run.Status != "completed" {
				continue
			}
			runs = append(runs, trendRun{Start: run.StartedAt, Size: run.SizeBytes, Category: cat[j.ID]})
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"period": period,
		"bucket": bucket,
		"points": bucketTrend(runs, bucket),
	})
}

// dominantCategory mirrors the frontend SizeChart classification: the job's
// most common item-type category (ties resolve by canonical order).
func dominantCategory(items []db.JobItem) string {
	if len(items) == 0 {
		return "other"
	}
	order := []string{"containers", "vms", "folders", "flash", "other"}
	counts := map[string]int{}
	for _, it := range items {
		counts[classifyItemType(it.ItemType)]++
	}
	best, bestN := "other", -1
	for _, c := range order {
		if counts[c] > bestN {
			bestN = counts[c]
			best = c
		}
	}
	return best
}

func classifyItemType(itemType string) string {
	switch strings.ToLower(itemType) {
	case "container", "docker":
		return "containers"
	case "vm", "libvirt":
		return "vms"
	case "folder", "folders", "file", "files", "path":
		return "folders"
	case "flash", "usb", "boot":
		return "flash"
	default:
		return "other"
	}
}

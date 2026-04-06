package diagnostics

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/google/uuid"
	"github.com/ruaan-deysel/vault/internal/db"
)

// RunnerStatus holds a snapshot of the runner state for diagnostics.
type RunnerStatus struct {
	Active          bool
	JobID           int64
	JobName         string
	RunType         string
	ItemsTotal      int
	ItemsDone       int
	ItemsFailed     int
	CurrentItem     string
	CurrentItemType string
}

// StatusFunc returns a snapshot of the current runner status.
type StatusFunc func() RunnerStatus

// Collector gathers diagnostic data from the system.
type Collector struct {
	db       *db.DB
	statusFn StatusFunc
	version  string
}

// NewCollector creates a new diagnostic collector.
func NewCollector(database *db.DB, statusFn StatusFunc, version string) *Collector {
	return &Collector{
		db:       database,
		statusFn: statusFn,
		version:  version,
	}
}

// Collect gathers a complete diagnostic bundle from the system.
func (c *Collector) Collect() (*DiagnosticBundle, error) {
	correlationID := uuid.New().String()
	now := time.Now().UTC()
	hostname, _ := os.Hostname()

	bundle := &DiagnosticBundle{
		GeneratedAt:   now,
		CorrelationID: correlationID,
		System: SystemInfo{
			Version:   c.version,
			GoVersion: runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
			Hostname:  hostname,
		},
	}

	var entries []DiagnosticEntry

	// Database info.
	bundle.Database = c.collectDatabaseInfo()
	entries = append(entries, DiagnosticEntry{
		Timestamp:     now,
		Level:         LevelInfo,
		Message:       "Database info collected",
		CorrelationID: correlationID,
		Service:       "database",
		Host:          hostname,
		Context: map[string]string{
			"path": bundle.Database.Path,
			"mode": bundle.Database.Mode,
		},
	})

	// Storage destinations.
	if dests, err := c.db.ListStorageDestinations(); err == nil {
		for _, d := range dests {
			bundle.Storage = append(bundle.Storage, StorageInfo{
				ID:     d.ID,
				Name:   d.Name,
				Type:   d.Type,
				Config: RedactJSON(d.Config),
			})
		}
		entries = append(entries, DiagnosticEntry{
			Timestamp:     now,
			Level:         LevelInfo,
			Message:       fmt.Sprintf("Found %d storage destination(s)", len(dests)),
			CorrelationID: correlationID,
			Service:       "storage",
			Host:          hostname,
		})
	} else {
		entries = append(entries, DiagnosticEntry{
			Timestamp:     now,
			Level:         LevelError,
			Message:       fmt.Sprintf("Failed to list storage destinations: %v", err),
			CorrelationID: correlationID,
			Service:       "storage",
			Host:          hostname,
		})
	}

	// Jobs with item counts.
	if jobs, err := c.db.ListJobs(); err == nil {
		for _, j := range jobs {
			ji := JobInfo{
				ID:       j.ID,
				Name:     j.Name,
				Schedule: j.Schedule,
				Enabled:  j.Enabled,
			}
			if items, err := c.db.GetJobItems(j.ID); err == nil {
				ji.ItemCount = len(items)
			}
			bundle.Jobs = append(bundle.Jobs, ji)
		}
		entries = append(entries, DiagnosticEntry{
			Timestamp:     now,
			Level:         LevelInfo,
			Message:       fmt.Sprintf("Found %d job(s)", len(jobs)),
			CorrelationID: correlationID,
			Service:       "jobs",
			Host:          hostname,
		})
	} else {
		entries = append(entries, DiagnosticEntry{
			Timestamp:     now,
			Level:         LevelError,
			Message:       fmt.Sprintf("Failed to list jobs: %v", err),
			CorrelationID: correlationID,
			Service:       "jobs",
			Host:          hostname,
		})
	}

	// Recent runs (last 50 across all jobs).
	if runs, err := c.db.ListRecentRuns(50); err == nil {
		for _, r := range runs {
			ri := RunInfo{
				ID:           r.ID,
				JobID:        r.JobID,
				Status:       r.Status,
				RunType:      r.RunType,
				StartedAt:    r.StartedAt,
				CompletedAt:  r.CompletedAt,
				ItemsTotal:   r.ItemsTotal,
				ItemsSuccess: r.ItemsDone,
				ItemsFailed:  r.ItemsFailed,
			}
			if r.Log != "" {
				ri.ErrorMessage = r.Log
			}
			bundle.Runs = append(bundle.Runs, ri)
		}
		entries = append(entries, DiagnosticEntry{
			Timestamp:     now,
			Level:         LevelInfo,
			Message:       fmt.Sprintf("Collected %d recent run(s)", len(runs)),
			CorrelationID: correlationID,
			Service:       "runner",
			Host:          hostname,
		})
	} else {
		entries = append(entries, DiagnosticEntry{
			Timestamp:     now,
			Level:         LevelError,
			Message:       fmt.Sprintf("Failed to list recent runs: %v", err),
			CorrelationID: correlationID,
			Service:       "runner",
			Host:          hostname,
		})
	}

	// Activity logs (last 200).
	if logs, err := c.db.ListActivityLogs(200, ""); err == nil {
		for _, l := range logs {
			bundle.Activity = append(bundle.Activity, ActivityInfo{
				ID:        l.ID,
				Level:     l.Level,
				Message:   l.Message,
				CreatedAt: l.CreatedAt,
			})
		}
		entries = append(entries, DiagnosticEntry{
			Timestamp:     now,
			Level:         LevelInfo,
			Message:       fmt.Sprintf("Collected %d activity log(s)", len(logs)),
			CorrelationID: correlationID,
			Service:       "activity",
			Host:          hostname,
		})
	} else {
		entries = append(entries, DiagnosticEntry{
			Timestamp:     now,
			Level:         LevelWarn,
			Message:       fmt.Sprintf("Failed to list activity logs: %v", err),
			CorrelationID: correlationID,
			Service:       "activity",
			Host:          hostname,
		})
	}

	// Runner status.
	if c.statusFn != nil {
		s := c.statusFn()
		bundle.Runner = RunnerInfo(s)
	}

	// Replication sources.
	if sources, err := c.db.ListReplicationSources(); err == nil {
		for _, s := range sources {
			bundle.Replication = append(bundle.Replication, ReplicationInfo{
				ID:       s.ID,
				Name:     s.Name,
				URL:      RedactURL(s.URL),
				Enabled:  s.Enabled,
				Interval: s.Schedule,
			})
		}
		entries = append(entries, DiagnosticEntry{
			Timestamp:     now,
			Level:         LevelInfo,
			Message:       fmt.Sprintf("Found %d replication source(s)", len(sources)),
			CorrelationID: correlationID,
			Service:       "replication",
			Host:          hostname,
		})
	} else {
		entries = append(entries, DiagnosticEntry{
			Timestamp:     now,
			Level:         LevelWarn,
			Message:       fmt.Sprintf("Failed to list replication sources: %v", err),
			CorrelationID: correlationID,
			Service:       "replication",
			Host:          hostname,
		})
	}

	bundle.Entries = entries
	return bundle, nil
}

// collectDatabaseInfo gathers database file information.
func (c *Collector) collectDatabaseInfo() DatabaseInfo {
	info := DatabaseInfo{
		Path: c.db.Path(),
		Mode: "legacy_usb",
	}

	// Detect hybrid mode by checking for a snapshot path setting.
	if override, err := c.db.GetSetting("snapshot_path_override", ""); err == nil && override != "" {
		info.Mode = "hybrid"
	} else if _, err := os.Stat("/mnt/cache"); err == nil {
		info.Mode = "hybrid"
	}

	if fi, err := os.Stat(c.db.Path()); err == nil {
		info.SizeBytes = fi.Size()
	}

	return info
}

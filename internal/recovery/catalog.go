package recovery

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/runner"
)

type CatalogOptions struct {
	JobID       int64 `json:"job_id,omitempty" jsonschema:"Job ID to filter by"`
	LimitPerJob int   `json:"limit_per_job,omitempty" jsonschema:"Restore Points per Job, default 200 and maximum 1000"`
}

var ErrInvalidCatalogOptions = errors.New("job_id must be positive and limit_per_job must be between 1 and 1000 when supplied")

type Catalog struct {
	Jobs []CatalogJob `json:"jobs"`
}

type CatalogJob struct {
	ID            int64          `json:"id"`
	Name          string         `json:"name"`
	Enabled       bool           `json:"enabled"`
	Replicated    bool           `json:"replicated"`
	RestorePoints []CatalogPoint `json:"restore_points"`
	Truncated     bool           `json:"truncated"`
}

type CatalogPoint struct {
	ID                   int64     `json:"id"`
	JobID                int64     `json:"job_id"`
	JobRunID             int64     `json:"job_run_id"`
	BackupType           string    `json:"backup_type"`
	CreatedAt            time.Time `json:"created_at"`
	SizeBytes            int64     `json:"size_bytes"`
	ParentRestorePointID int64     `json:"parent_restore_point_id"`
	ChainStatus          string    `json:"chain_status"`
	ChainDepth           int       `json:"chain_depth"`
	ChainWarning         string    `json:"chain_warning,omitempty"`
	Membership           string    `json:"membership"`
	ItemNames            []string  `json:"item_names"`
}

// BuildCatalog projects recorded recovery metadata without opening backup storage.
func BuildCatalog(ctx context.Context, database *db.DB, opts CatalogOptions) (Catalog, error) {
	if opts.JobID < 0 || opts.LimitPerJob < 0 || opts.LimitPerJob > 1000 {
		return Catalog{}, ErrInvalidCatalogOptions
	}
	if opts.LimitPerJob == 0 {
		opts.LimitPerJob = 200
	}
	jobs, err := database.ListJobsContext(ctx)
	if err != nil {
		return Catalog{}, err
	}

	result := Catalog{Jobs: make([]CatalogJob, 0, len(jobs))}
	for _, job := range jobs {
		if opts.JobID != 0 && opts.JobID != job.ID {
			continue
		}
		points, err := database.ListRestorePointsContext(ctx, job.ID)
		if err != nil {
			return Catalog{}, err
		}
		byID := make(map[int64]db.RestorePoint, len(points))
		for _, point := range points {
			byID[point.ID] = point
		}
		count := min(len(points), opts.LimitPerJob)
		entry := CatalogJob{
			ID: job.ID, Name: catalogLabel(job.Name), Enabled: job.Enabled, Replicated: job.SourceID > 0,
			RestorePoints: make([]CatalogPoint, 0, count), Truncated: count < len(points),
		}
		for _, point := range points[:count] {
			if err := ctx.Err(); err != nil {
				return Catalog{}, err
			}
			entry.RestorePoints = append(entry.RestorePoints, catalogPoint(point, byID))
		}
		result.Jobs = append(result.Jobs, entry)
	}
	return result, nil
}

func catalogPoint(point db.RestorePoint, byID map[int64]db.RestorePoint) CatalogPoint {
	status, depth, warning := runner.RestorePointChainHealth(point, byID)
	names, known := point.BackedUpItems()
	entry := CatalogPoint{
		ID: point.ID, JobID: point.JobID, JobRunID: point.JobRunID,
		BackupType: catalogLabel(point.BackupType), CreatedAt: point.CreatedAt, SizeBytes: point.SizeBytes,
		ParentRestorePointID: point.ParentRestorePointID, ChainStatus: status, ChainDepth: depth, ChainWarning: warning,
		Membership: "unknown", ItemNames: make([]string, 0, len(names)),
	}
	if known {
		entry.Membership = "recorded"
	}
	for name := range names {
		entry.ItemNames = append(entry.ItemNames, catalogLabel(name))
	}
	slices.Sort(entry.ItemNames)
	entry.ItemNames = slices.Compact(entry.ItemNames)
	return entry
}

// Some older folder records use the source path as the item name. Labels
// from imported records can also contain URLs or a pasted Assistant Key.
func catalogLabel(label string) string {
	candidate := strings.TrimSpace(label)
	if strings.HasPrefix(candidate, "/") || strings.Contains(candidate, "://") || strings.Contains(candidate, crypto.AssistantKeyPrefix) {
		return "[redacted]"
	}
	return label
}

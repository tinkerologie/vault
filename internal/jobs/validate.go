package jobs

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/scheduler"
)

// MaxJobNameLen bounds the job name so a pathological value cannot bloat the
// row or break list rendering. Exported because it is part of what a caller
// must know to submit a Job that will be accepted.
const MaxJobNameLen = 255

// retryMaxLimit mirrors the bound the UI enforces on the same input, so an API
// client cannot persist a value the UI would reject.
const retryMaxLimit = 10

// maxParallelUploadsLimit is the ceiling MaxParallelUploads is clamped to.
// Job.EffectiveUploadConcurrency bounds it to [1,16] again at use time.
const maxParallelUploadsLimit = 16

// jobEnums lists the accepted values for each free-string job field. Anything
// outside these sets used to persist verbatim and only fail later inside the
// engine, where the error is far from the user's action.
var jobEnums = map[string][]string{
	"backup_type_chain": {"full", "incremental", "differential"},
	"compression":       {"none", "gzip", "zstd"},
	"encryption":        {"none", "age"},
	// "stop_all" is the canonical value everywhere else — config.ContainerStopAll,
	// the runner's batch path, and the value BackupModeSelector.svelte submits.
	// This list said "all_at_once", so saving a job in Batch mode was rejected
	// (issue #261).
	"container_mode": {"one_by_one", "stop_all"},
	"vm_mode":        {"snapshot", "cold"},
	"verify_mode":    {"quick", "deep"},
	"notify_on":      {"always", "failure", "never"},
}

// validateEnum reports whether value is allowed for field. An empty value is
// accepted: the DB layer applies the column default, and the wizard omits
// fields that do not apply to the selected item types.
func validateEnum(field, value string) error {
	if value == "" {
		return nil
	}
	allowed, ok := jobEnums[field]
	if !ok {
		return nil
	}
	if slices.Contains(allowed, value) {
		return nil
	}
	return invalid(field, "invalid %s %q (expected one of: %s)", field, value, strings.Join(allowed, ", "))
}

// normalize validates a Job's caller-supplied fields and rewrites the ones
// that are clamped rather than rejected, in place. It returns a
// *ValidationError for anything the caller must fix.
//
// Validation and normalisation are one step on purpose: every field that is
// checked is also the field that is trimmed or clamped, and splitting them
// invites a caller to run one without the other — which is how a
// whitespace-only schedule used to reach the scheduler.
func normalize(job *db.Job) error {
	job.Name = strings.TrimSpace(job.Name)
	if job.Name == "" {
		return invalid("name", "name is required")
	}
	if len(job.Name) > MaxJobNameLen {
		return invalid("name", "name must be %d characters or fewer", MaxJobNameLen)
	}
	// Normalize the schedule in place so a whitespace-only value is stored as
	// "" (manual-only). Otherwise it would pass validation (ValidateSchedule
	// trims internally) yet persist as non-empty, and the scheduler's
	// `Schedule != ""` check would then try to cron-parse "   ", fail, and
	// leave the job marked scheduled but never actually running.
	job.Schedule = strings.TrimSpace(job.Schedule)
	if err := scheduler.ValidateSchedule(job.Schedule); err != nil {
		return invalidCause("schedule", err, "invalid schedule: %s", err.Error())
	}
	for field, value := range map[string]string{
		"backup_type_chain": job.BackupTypeChain,
		"compression":       job.Compression,
		"encryption":        job.Encryption,
		"container_mode":    job.ContainerMode,
		"vm_mode":           job.VMMode,
		"verify_mode":       job.VerifyMode,
		"notify_on":         job.NotifyOn,
	} {
		if err := validateEnum(field, value); err != nil {
			return err
		}
	}

	if job.RetentionCount < 0 {
		return invalid("retention_count", "retention_count must not be negative")
	}
	if job.RetentionDays < 0 {
		return invalid("retention_days", "retention_days must not be negative")
	}
	if job.RetryMaxOverride != nil && (*job.RetryMaxOverride < 0 || *job.RetryMaxOverride > retryMaxLimit) {
		return invalid("retry_max_override",
			"retry_max_override must be between 0 and %d", retryMaxLimit)
	}

	// max_parallel_uploads is deliberately CLAMPED rather than rejected —
	// Job.EffectiveUploadConcurrency bounds it to [1,16] at use time, and
	// rejecting here would break that contract.
	if job.MaxParallelUploads > maxParallelUploadsLimit {
		job.MaxParallelUploads = maxParallelUploadsLimit
	}
	if job.MaxParallelUploads < 0 {
		job.MaxParallelUploads = 0
	}

	job.CompressionLevel = normalizeCompressionLevel(job.Compression, job.CompressionLevel)
	return nil
}

// normalizeCompressionLevel constrains the level to the known set so junk values
// never persist. The level only applies to gzip/zstd, so it is cleared for any
// other algorithm (none/unknown) and for an empty/"default"/unknown level (the
// engine's default); otherwise the recognised fastest/better/best is kept.
func normalizeCompressionLevel(compression, level string) string {
	if compression != "gzip" && compression != "zstd" {
		return ""
	}
	switch level {
	case "fastest", "better", "best":
		return level
	default:
		return ""
	}
}

// folderSourcePath extracts the on-array source path of a folder item, from
// its settings JSON if present and otherwise from an absolute ItemID/ItemName.
func folderSourcePath(item db.JobItem) (string, error) {
	var settings struct {
		Path string `json:"path"`
	}
	if item.Settings != "" {
		if err := json.Unmarshal([]byte(item.Settings), &settings); err != nil {
			return "", err
		}
	}
	if path := strings.TrimSpace(settings.Path); path != "" {
		return path, nil
	}
	if path := strings.TrimSpace(item.ItemID); strings.HasPrefix(path, "/") {
		return path, nil
	}
	if path := strings.TrimSpace(item.ItemName); strings.HasPrefix(path, "/") {
		return path, nil
	}
	return "", nil
}

// pathsOverlap reports whether two paths are equal or one is nested inside the
// other (after cleaning).
func pathsOverlap(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || isSubPath(a, b) || isSubPath(b, a)
}

// isSubPath reports whether child is strictly inside parent.
func isSubPath(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolvePath resolves symlinks best-effort so a symlinked destination inside a
// source tree can't slip past the overlap check. When the leaf doesn't exist
// yet (e.g. a not-yet-created backup destination) it resolves the longest
// existing ancestor — catching a symlinked parent — and rejoins the remaining
// tail, falling back to a lexical clean only if nothing in the chain exists.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	cur, tail := p, ""
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, tail)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p // reached the root without finding an existing path
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

// folderSourceOverlap returns a reason and true when destPath is the same as,
// inside, or a parent of any folder/flash item's source path — the classic
// "backing itself up" footgun where each run archives the previous run's output
// and the source grows without bound. Flash items are folder-typed, so the
// "folder" guard covers them. An empty destPath (a remote destination with no
// on-array path) never overlaps. Symlinks are resolved so a symlinked
// destination inside a source can't evade the check.
func folderSourceOverlap(destPath string, items []db.JobItem) (string, bool) {
	if strings.TrimSpace(destPath) == "" {
		return "", false
	}
	dest := resolvePath(destPath)
	for _, it := range items {
		if it.ItemType != "folder" {
			continue
		}
		sourcePath, err := folderSourcePath(it)
		if err != nil || sourcePath == "" {
			continue
		}
		if pathsOverlap(resolvePath(sourcePath), dest) {
			return fmt.Sprintf(
				"backup destination %q overlaps the backup source %q — the job would back up into its own source; choose a destination outside the source tree",
				filepath.Clean(destPath), filepath.Clean(sourcePath)), true
		}
	}
	return "", false
}

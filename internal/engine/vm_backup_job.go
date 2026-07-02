package engine

import (
	"fmt"
	"os"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
)

// These helpers are consumed by the linux-tagged VM implementation, but this
// file is untagged so the backup-job outcome logic can still be exercised
// from platform-neutral tests and linted on the host OS.
var (
	_ = resolveVanishedBackupJob
	_ = backupJobError
	_ = backupArtifactsExist
	_ = describeMissingBackupArtifacts
	_ = backupProgressPercent
	_ = backupProgressMessage
)

// resolveVanishedBackupJob decides the backup outcome once the active job is
// gone (the current-job query reported DomainJobNone or failed) based on the
// completed-job stats query. libvirt reports "no completed-job record" as a
// successful query with job type DomainJobNone, so that case carries no
// verdict (issue #160: it must not be treated as a failure); the caller falls
// back to the artifacts on disk. done=false means "no verdict, keep polling".
func resolveVanishedBackupJob(completedType libvirt.DomainJobType, completedParams []libvirt.TypedParam, completedErr error, artifactsOK bool) (done bool, err error) {
	if completedErr == nil && completedType != libvirt.DomainJobNone {
		return true, backupJobError(completedType, completedParams)
	}
	if artifactsOK {
		return true, nil
	}
	return false, nil
}

func backupJobError(jobType libvirt.DomainJobType, params []libvirt.TypedParam) error {
	success, ok := typedParamBool(params, "success")

	errMsg := typedParamString(params, "errmsg", "error")
	if errMsg == "" {
		errMsg = jobType.String()
	}

	switch jobType {
	case libvirt.DomainJobCompleted:
		// The "success" typed param is only authoritative for completed
		// records; failed/cancelled job types are errors regardless.
		if ok && !success {
			return fmt.Errorf("backup job failed: %s", errMsg)
		}
		return nil
	case libvirt.DomainJobFailed:
		return fmt.Errorf("backup job failed: %s", errMsg)
	case libvirt.DomainJobCancelled:
		return fmt.Errorf("backup job cancelled: %s", errMsg)
	default:
		return fmt.Errorf("backup job ended unexpectedly: %s", errMsg)
	}
}

// missingBackupArtifacts returns the expected backup outputs that are absent
// or empty on disk.
func missingBackupArtifacts(artifacts []vmBackupArtifact) []string {
	missing := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		info, err := os.Stat(artifact.TargetPath)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			missing = append(missing, artifact.BackupFile)
		}
	}
	return missing
}

func backupArtifactsExist(artifacts []vmBackupArtifact) bool {
	return len(artifacts) > 0 && len(missingBackupArtifacts(artifacts)) == 0
}

// describeMissingBackupArtifacts names the expected backup outputs that are
// absent or empty, for the error raised when a backup job vanishes without
// leaving a completion record or its artifacts.
func describeMissingBackupArtifacts(artifacts []vmBackupArtifact) string {
	missing := missingBackupArtifacts(artifacts)
	if len(missing) == 0 {
		return "all expected artifacts present"
	}
	return "missing or empty backup artifacts: " + strings.Join(missing, ", ")
}

// backupProgressPercent maps libvirt's byte counters into the 35–85 slice of
// the overall backup progress bar (earlier percentages cover preparation,
// later ones checkpointing and NVRAM copy). Without usable counters it parks
// the bar mid-range.
func backupProgressPercent(params []libvirt.TypedParam) int {
	processed, okProcessed := typedParamUint64(params, "fileprocessed", "diskprocessed", "dataprocessed")
	total, okTotal := typedParamUint64(params, "filetotal", "disktotal", "datatotal")
	if !okProcessed || !okTotal || total == 0 {
		return 50
	}

	percent := int((processed * 100) / total)
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	return 35 + (percent * 50 / 100)
}

func backupProgressMessage(params []libvirt.TypedParam) string {
	processed, okProcessed := typedParamUint64(params, "fileprocessed", "diskprocessed", "dataprocessed")
	total, okTotal := typedParamUint64(params, "filetotal", "disktotal", "datatotal")
	if okProcessed && okTotal && total > 0 {
		return fmt.Sprintf("backup in progress: %s/%s", humanizeBytes(float64(processed)), humanizeBytes(float64(total)))
	}

	return "backup in progress"
}

func typedParamBool(params []libvirt.TypedParam, keys ...string) (bool, bool) {
	for _, param := range params {
		normalized := normalizeTypedParamField(param.Field)
		for _, key := range keys {
			if normalized == key {
				return typedParamValueBool(param.Value), true
			}
		}
	}

	return false, false
}

func typedParamUint64(params []libvirt.TypedParam, keys ...string) (uint64, bool) {
	for _, param := range params {
		normalized := normalizeTypedParamField(param.Field)
		for _, key := range keys {
			if normalized != key {
				continue
			}

			switch value := param.Value.I.(type) {
			case uint64:
				return value, true
			case int64:
				if value >= 0 {
					return uint64(value), true
				}
			case uint32:
				return uint64(value), true
			case int32:
				if value >= 0 {
					return uint64(value), true
				}
			case int:
				if value >= 0 {
					return uint64(value), true
				}
			case uint:
				return uint64(value), true
			}
		}
	}

	return 0, false
}

func typedParamString(params []libvirt.TypedParam, keys ...string) string {
	for _, param := range params {
		normalized := normalizeTypedParamField(param.Field)
		for _, key := range keys {
			if normalized != key {
				continue
			}

			if value, ok := param.Value.I.(string); ok {
				return value
			}
		}
	}

	return ""
}

func typedParamValueBool(value libvirt.TypedParamValue) bool {
	switch typed := value.I.(type) {
	case bool:
		return typed
	case int32:
		return typed != 0
	case uint32:
		return typed != 0
	case int:
		return typed != 0
	case uint:
		return typed != 0
	default:
		return false
	}
}

func normalizeTypedParamField(field string) string {
	var builder strings.Builder
	builder.Grow(len(field))
	for _, r := range field {
		if r >= 'A' && r <= 'Z' {
			builder.WriteRune(r + ('a' - 'A'))
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

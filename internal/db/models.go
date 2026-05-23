package db

import (
	"time"
)

type Job struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	Enabled           bool   `json:"enabled"`
	Schedule          string `json:"schedule"`
	BackupTypeChain   string `json:"backup_type_chain"`
	RetentionCount    int    `json:"retention_count"`
	RetentionDays     int    `json:"retention_days"`
	Compression       string `json:"compression"`
	Encryption        string `json:"encryption"`
	ContainerMode     string `json:"container_mode"`
	VMMode            string `json:"vm_mode"`
	PreScript         string `json:"pre_script"`
	PostScript        string `json:"post_script"`
	NotifyOn          string `json:"notify_on"`
	VerifyBackup      bool   `json:"verify_backup"`
	StorageDestID     int64  `json:"storage_dest_id"`
	SourceID          int64  `json:"source_id"`
	DeferRemoteUpload bool   `json:"defer_remote_upload"`
	// GFS (grandfather-father-son) retention. Each defaults to 0 (disabled).
	// If any of the five is > 0 the runner uses GFS classification and
	// ignores RetentionCount / RetentionDays.
	KeepLatest  int `json:"keep_latest"`
	KeepDaily   int `json:"keep_daily"`
	KeepWeekly  int `json:"keep_weekly"`
	KeepMonthly int `json:"keep_monthly"`
	KeepYearly  int `json:"keep_yearly"`
	// Scheduled verification (Feature A). VerifySchedule is a cron
	// expression; empty means no scheduled verification. VerifyMode is
	// "quick" or "deep".
	VerifySchedule string `json:"verify_schedule"`
	VerifyMode     string `json:"verify_mode"`
	// Retry overrides (Task 7). nil means "use global default" from
	// settings (retry_max_default / retry_delays_default).
	// RetryDelaysOverride stores a JSON array of seconds, e.g. "[60,300]".
	// Pointer types so the JSON API emits null (not {Valid,Int64}) for unset.
	RetryMaxOverride    *int64  `json:"retry_max_override"`
	RetryDelaysOverride *string `json:"retry_delays_override"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

type JobItem struct {
	ID        int64  `json:"id"`
	JobID     int64  `json:"job_id"`
	ItemType  string `json:"item_type"`
	ItemName  string `json:"item_name"`
	ItemID    string `json:"item_id"`
	Settings  string `json:"settings"`
	SortOrder int    `json:"sort_order"`
}

type JobRun struct {
	ID              int64      `json:"id"`
	JobID           int64      `json:"job_id"`
	Status          string     `json:"status"`
	BackupType      string     `json:"backup_type"`
	RunType         string     `json:"run_type"`
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	Log             string     `json:"log"`
	ItemsTotal      int        `json:"items_total"`
	ItemsDone       int        `json:"items_done"`
	ItemsFailed     int        `json:"items_failed"`
	SizeBytes       int64      `json:"size_bytes"`
	DurationSeconds *int       `json:"duration_seconds"`
	// Retry fields (Task 7). RetryOfRunID identifies the original failed
	// run this is a retry of (nil for non-retry runs). RetryAttempt is
	// 0-indexed (0 = first retry, 1 = second, ...). RetryNextAt is set
	// on a failed run when the scheduler should re-fire it later.
	// Pointer types so the JSON API emits null for unset.
	RetryOfRunID *int64     `json:"retry_of_run_id"`
	RetryAttempt int        `json:"retry_attempt"`
	RetryNextAt  *time.Time `json:"retry_next_at"`
}

type RestorePoint struct {
	ID                   int64     `json:"id"`
	JobRunID             int64     `json:"job_run_id"`
	JobID                int64     `json:"job_id"`
	BackupType           string    `json:"backup_type"`
	StoragePath          string    `json:"storage_path"`
	Metadata             string    `json:"metadata"`
	SizeBytes            int64     `json:"size_bytes"`
	ParentRestorePointID int64     `json:"parent_restore_point_id"`
	SourceID             int64     `json:"source_id"`
	ManifestID           []byte    `json:"manifest_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

type StorageDestination struct {
	ID                    int64      `json:"id"`
	Name                  string     `json:"name"`
	Type                  string     `json:"type"`
	Config                string     `json:"config"`
	DedupEnabled          bool       `json:"dedup_enabled"`
	LastHealthCheckAt     *time.Time `json:"last_health_check_at"`
	LastHealthCheckStatus string     `json:"last_health_check_status"`
	LastHealthCheckError  string     `json:"last_health_check_error"`
	ConsecutiveFailures   int        `json:"consecutive_failures"`
	BreakerState          string     `json:"breaker_state"`
	BreakerOpenedAt       *time.Time `json:"breaker_opened_at,omitempty"`
	BackupDatabaseEnabled bool       `json:"backup_database_enabled"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type ActivityLogEntry struct {
	ID        int64     `json:"id"`
	Level     string    `json:"level"`
	Category  string    `json:"category"`
	Message   string    `json:"message"`
	Details   string    `json:"details"`
	CreatedAt time.Time `json:"created_at"`
}

// VerifyRun records one execution of restore-point verification. Mode is
// "quick" (storage HEAD + size compare) or "deep" (full read + SHA-256
// re-compute). Status transitions running -> passed | failed | cancelled.
type VerifyRun struct {
	ID             int64      `json:"id"`
	RestorePointID int64      `json:"restore_point_id"`
	Mode           string     `json:"mode"`
	Status         string     `json:"status"`
	FilesChecked   int        `json:"files_checked"`
	FilesFailed    int        `json:"files_failed"`
	BytesRead      int64      `json:"bytes_read"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at"`
	ErrorSummary   string     `json:"error_summary"`
}

// ReplicationSource represents a replication target (remote Vault server)
// where local backups are pushed for disaster recovery.
type ReplicationSource struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Type           string     `json:"type"`
	URL            string     `json:"url"`
	Config         string     `json:"config"`
	StorageDestID  int64      `json:"storage_dest_id"`
	Schedule       string     `json:"schedule"`
	Enabled        bool       `json:"enabled"`
	LastSyncAt     *time.Time `json:"last_sync_at"`
	LastSyncStatus string     `json:"last_sync_status"`
	LastSyncError  string     `json:"last_sync_error"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

package diagnostics

import "time"

// Diagnostic severity levels.
const (
	LevelError = "error"
	LevelWarn  = "warn"
	LevelInfo  = "info"
	LevelDebug = "debug"
)

// DiagnosticBundle is the top-level structure for all diagnostic data.
//
// Layout note: PackageAsZip splits this into multiple files in the
// output ZIP (vault.log, jobs.json, storage.json, …) for human
// skim-ability. The full struct is still serialized to diagnostics.json
// as the top-level overview so support tooling can grep one file.
type DiagnosticBundle struct {
	GeneratedAt   time.Time         `json:"generated_at"`
	CorrelationID string            `json:"correlation_id"`
	System        SystemInfo        `json:"system"`
	Database      DatabaseInfo      `json:"database"`
	Settings      SettingsInfo      `json:"settings"`
	Storage       []StorageInfo     `json:"storage"`
	Jobs          []JobInfo         `json:"jobs"`
	Runs          []RunInfo         `json:"runs"`
	Activity      []ActivityInfo    `json:"activity"`
	Runner        RunnerInfo        `json:"runner"`
	Replication   []ReplicationInfo `json:"replication"`
	Entries       []DiagnosticEntry `json:"entries"`

	// LogTail holds the most recent N bytes of daemon stdout/stderr,
	// already passed through RedactLogLines. Embedded only in the
	// top-level struct for ergonomic JSON round-tripping; PackageAsZip
	// writes it to its own `vault.log` file in the output archive and
	// clears this field on the JSON copy to keep diagnostics.json
	// scannable.
	LogTail string `json:"log_tail,omitempty"`
}

// SystemInfo holds environment metadata.
type SystemInfo struct {
	GoVersion string      `json:"go_version"`
	OS        string      `json:"os"`
	Arch      string      `json:"arch"`
	Hostname  string      `json:"hostname"`
	Version   string      `json:"vault_version"`
	Disks     []DiskUsage `json:"disks,omitempty"`
}

// DiskUsage reports filesystem capacity at a probed path. The collector
// probes a fixed set of paths Vault depends on (DB location, staging
// directories, /boot for flash backups, /tmp). A missing path is
// silently omitted from the slice; a stat failure shows up as an entry
// with Error set so support tickets distinguish "not mounted" from
// "permission denied".
type DiskUsage struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedPct    int    `json:"used_pct"`
	Error      string `json:"error,omitempty"`
}

// SettingsInfo holds a snapshot of operationally-interesting settings,
// fully redacted of secrets. Booleans are presented for credential
// existence rather than the credential itself ("encryption_configured"
// rather than the passphrase, "api_key_configured" rather than the key
// or its hash).
type SettingsInfo struct {
	EncryptionConfigured   bool   `json:"encryption_configured"`
	APIKeyConfigured       bool   `json:"api_key_configured"`
	StagingDirOverride     string `json:"staging_dir_override,omitempty"`
	SnapshotPathOverride   string `json:"snapshot_path_override,omitempty"`
	ContainerBackupEnabled bool   `json:"container_backup_enabled"`
	VMBackupEnabled        bool   `json:"vm_backup_enabled"`
	FlashBackupEnabled     bool   `json:"flash_backup_enabled"`
	TimeFormat             string `json:"time_format,omitempty"`
	NotificationProvider   string `json:"notification_provider,omitempty"`
}

// DatabaseInfo holds database state.
type DatabaseInfo struct {
	Path        string `json:"path"`
	SizeBytes   int64  `json:"size_bytes"`
	Mode        string `json:"mode"`
	BusyTimeout string `json:"busy_timeout"`
}

// StorageInfo holds a redacted storage destination summary.
type StorageInfo struct {
	ID                    int64      `json:"id"`
	Name                  string     `json:"name"`
	Type                  string     `json:"type"`
	Config                string     `json:"config"` // redacted JSON
	DedupEnabled          bool       `json:"dedup_enabled"`
	LastHealthCheckAt     *time.Time `json:"last_health_check_at,omitempty"`
	LastHealthCheckStatus string     `json:"last_health_check_status,omitempty"`
	LastHealthCheckError  string     `json:"last_health_check_error,omitempty"`
}

// JobInfo holds full job configuration plus a redacted view of every
// item. Job item settings (container exclusion patterns, folder paths,
// VM mode, ZFS dataset names) are critical for triaging "my backup
// isn't doing what I expect" reports.
type JobInfo struct {
	ID                int64         `json:"id"`
	Name              string        `json:"name"`
	Schedule          string        `json:"schedule"`
	Enabled           bool          `json:"enabled"`
	BackupTypeChain   string        `json:"backup_type_chain"`
	Compression       string        `json:"compression"`
	HasEncryption     bool          `json:"has_encryption"`
	ContainerMode     string        `json:"container_mode,omitempty"`
	VMMode            string        `json:"vm_mode,omitempty"`
	NotifyOn          string        `json:"notify_on,omitempty"`
	VerifyBackup      bool          `json:"verify_backup"`
	VerifySchedule    string        `json:"verify_schedule,omitempty"`
	VerifyMode        string        `json:"verify_mode,omitempty"`
	DeferRemoteUpload bool          `json:"defer_remote_upload"`
	RetentionCount    int           `json:"retention_count"`
	RetentionDays     int           `json:"retention_days"`
	KeepLatest        int           `json:"keep_latest,omitempty"`
	KeepDaily         int           `json:"keep_daily,omitempty"`
	KeepWeekly        int           `json:"keep_weekly,omitempty"`
	KeepMonthly       int           `json:"keep_monthly,omitempty"`
	KeepYearly        int           `json:"keep_yearly,omitempty"`
	StorageDestID     int64         `json:"storage_dest_id"`
	ItemCount         int           `json:"item_count"`
	Items             []JobItemInfo `json:"items,omitempty"`
}

// JobItemInfo describes one backup target inside a job. Settings are
// passed through RedactJSON so any embedded credentials (rare but
// possible — e.g. a custom hook script with inline auth) are scrubbed
// before they hit a support ticket.
type JobItemInfo struct {
	ID       int64  `json:"id"`
	ItemType string `json:"item_type"`
	ItemName string `json:"item_name"`
	ItemID   string `json:"item_id"`
	Settings string `json:"settings"`
}

// RunInfo holds a summary of a job run.
//
// The runs table has one `log` column that is always a per-item JSON
// array (filled on success AND failure). The earlier diagnostics
// collector stuffed it into ErrorMessage, which lied for successful
// runs. We now expose it as the structured Log field and only fill
// ErrorMessages when the run actually failed.
type RunInfo struct {
	ID              int64      `json:"id"`
	JobID           int64      `json:"job_id"`
	Status          string     `json:"status"`
	BackupType      string     `json:"backup_type,omitempty"`
	RunType         string     `json:"run_type"`
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	DurationSeconds int        `json:"duration_seconds,omitempty"`
	ItemsTotal      int        `json:"items_total"`
	ItemsSuccess    int        `json:"items_success"`
	ItemsFailed     int        `json:"items_failed"`
	SizeBytes       int64      `json:"size_bytes,omitempty"`
	Log             string     `json:"log,omitempty"`            // raw per-item JSON
	ErrorMessages   []string   `json:"error_messages,omitempty"` // extracted when status=failed
}

// ActivityInfo holds an activity log entry.
type ActivityInfo struct {
	ID        int64     `json:"id"`
	Level     string    `json:"level"`
	Category  string    `json:"category,omitempty"`
	Message   string    `json:"message"`
	Details   string    `json:"details,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// RunnerInfo holds the current runner state.
type RunnerInfo struct {
	Active          bool   `json:"active"`
	JobID           int64  `json:"job_id,omitempty"`
	JobName         string `json:"job_name,omitempty"`
	RunType         string `json:"run_type,omitempty"`
	ItemsTotal      int    `json:"items_total,omitempty"`
	ItemsDone       int    `json:"items_done,omitempty"`
	ItemsFailed     int    `json:"items_failed,omitempty"`
	CurrentItem     string `json:"current_item,omitempty"`
	CurrentItemType string `json:"current_item_type,omitempty"`
}

// ReplicationInfo holds a redacted replication source summary.
type ReplicationInfo struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"` // redacted
	Enabled  bool   `json:"enabled"`
	Interval string `json:"interval"`
}

// DiagnosticEntry is a structured log entry within the bundle.
type DiagnosticEntry struct {
	Timestamp     time.Time         `json:"timestamp"`
	Level         string            `json:"level"`
	Message       string            `json:"message"`
	CorrelationID string            `json:"correlation_id,omitempty"`
	Service       string            `json:"service"`
	Host          string            `json:"host,omitempty"`
	Context       map[string]string `json:"context,omitempty"`
}

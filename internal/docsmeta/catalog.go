// Package docsmeta is the single source of truth for every user-configurable
// app setting and config-struct field in Vault. It exists so that:
//
//   - Default values for settings live in exactly one place. The db.GetSetting*
//     accessors take an explicit default at each call site; those call sites now
//     read the default from this registry (DefaultFor / DefaultInt / DefaultBool)
//     instead of hard-coding a literal, guaranteeing a single canonical value.
//   - A code-generated settings reference (and a drift test that keeps it honest)
//     can enumerate every setting and every exported config field with a
//     human-readable description, without scraping the code.
//
// This package holds only the catalog and its accessors — no I/O, no DB, no
// generation logic.
package docsmeta

import "strconv"

// Group classifies a setting for grouping in the generated reference.
type Group string

const (
	GroupGeneral       Group = "General"
	GroupBackup        Group = "Backup"
	GroupAnomaly       Group = "Anomaly Detection"
	GroupNotifications Group = "Notifications"
	// GroupInternal marks secrets and internal bookkeeping keys that are stored
	// in the settings table but are never rendered in the user-facing reference.
	GroupInternal Group = "Internal"
)

// SettingDoc describes one app-setting key.
//
// Default is always the string form as stored in the settings table (the DB
// column is TEXT). Type is a hint for rendering/parsing: "string", "int",
// "bool", "float", or "json". For every key, Default MUST equal the literal
// that the corresponding GetSetting* call site historically passed, so routing
// defaults through this registry does not change runtime behaviour.
type SettingDoc struct {
	Key         string
	Type        string
	Default     string
	Description string
	Group       Group
}

// AppSettings declares every app-setting key persisted in the settings table.
var AppSettings = []SettingDoc{
	// General
	{"history_retention_days", "int", "365", "Number of days of job-run history retained before pruning.", GroupGeneral},
	{"snapshot_path_override", "string", "", "Override for the directory used to stage filesystem snapshots. Empty uses the built-in default.", GroupGeneral},
	{"staging_dir_override", "string", "", "Override for the working directory used to stage archives before upload. Empty uses the built-in default.", GroupGeneral},
	{"storage_verbose_logging", "bool", "false", "When enabled, storage adapters log every operation for troubleshooting.", GroupGeneral},
	{"replication_enabled", "string", "", "Master toggle for the replication subsystem. Empty/false disables replication scheduling; \"true\" enables it.", GroupGeneral},

	// Backup engine
	{"retry_max_default", "int", "2", "Default maximum number of automatic retries for a failed backup when a job has no per-job override.", GroupBackup},
	{"retry_delays_default", "json", "[900,3600,14400]", "Default retry backoff schedule (JSON array of seconds) used when a job has no per-job override.", GroupBackup},
	{"breaker_fail_threshold", "int", "3", "Consecutive storage failures required before a destination's circuit breaker opens.", GroupBackup},
	{"breaker_close_successes", "int", "2", "Consecutive successes required to close an open circuit breaker and resume normal operation.", GroupBackup},
	{"dedup_compaction_min_dead_ratio", "float", "0.5", "Minimum fraction of dead (unreferenced) blocks in a dedup store before compaction runs.", GroupBackup},

	// Anomaly detection
	{"anomaly_detection_enabled", "bool", "true", "Master toggle for statistical anomaly detection on backup runs.", GroupAnomaly},
	{"anomaly_sensitivity_default", "string", "balanced", "Default detection sensitivity (\"strict\", \"balanced\", or \"permissive\") when a job or destination has no override.", GroupAnomaly},
	{"anomaly_notify_min_severity", "string", "critical", "Minimum anomaly severity that triggers a notification (\"info\", \"warning\", or \"critical\").", GroupAnomaly},

	// Notifications
	{"notifications_enabled", "bool", "true", "Master toggle for all outbound notifications.", GroupNotifications},
	{"discord_webhook_url", "string", "", "Discord webhook URL for notifications. Empty disables Discord delivery.", GroupNotifications},
	{"discord_notify_on", "string", "always", "When to send Discord notifications (\"always\" or \"failure\").", GroupNotifications},
	{"discord_bot_username", "string", "", "Optional override for the username shown on Discord webhook messages.", GroupNotifications},
	{"discord_bot_avatar_url", "string", "", "Optional override for the avatar shown on Discord webhook messages.", GroupNotifications},
	{"discord_mention_role_id", "string", "", "Discord role ID to mention on notifications. Empty disables role mentions.", GroupNotifications},
	{"discord_mention_on", "string", "never", "When to mention the configured Discord role (\"never\", \"failure\", or \"always\").", GroupNotifications},

	// Internal / secrets — persisted but never rendered in the reference.
	{"encryption_passphrase", "string", "", "Legacy plaintext backup encryption passphrase (migrated to a sealed value; retained for compatibility).", GroupInternal},
	{"encryption_passphrase_hash", "string", "", "Verification hash of the configured backup encryption passphrase.", GroupInternal},
	{"encryption_passphrase_sealed", "string", "", "Backup encryption passphrase sealed with the server key.", GroupInternal},
	{"api_key_hash", "string", "", "Verification hash of the configured API key.", GroupInternal},
	{"api_key_sealed", "string", "", "API key sealed with the server key.", GroupInternal},
}

// index maps each key to its SettingDoc for O(1) lookup. Built once at init.
var index = func() map[string]SettingDoc {
	m := make(map[string]SettingDoc, len(AppSettings))
	for _, s := range AppSettings {
		if _, dup := m[s.Key]; dup {
			panic("docsmeta: duplicate setting key: " + s.Key)
		}
		m[s.Key] = s
	}
	return m
}()

// DefaultFor returns the registered string default for key. It panics if the
// key is not registered in AppSettings — a programming error, since every key
// routed through this function must be declared above.
func DefaultFor(key string) string {
	s, ok := index[key]
	if !ok {
		panic("docsmeta: unregistered setting key: " + key)
	}
	return s.Default
}

// DefaultInt returns the registered default parsed as an int. It panics on an
// unregistered key or a malformed int default — both are programming errors the
// docsmeta drift test also guards against, so a panic here is unreachable in a
// correctly-registered tree.
func DefaultInt(key string) int {
	v, err := strconv.Atoi(DefaultFor(key))
	if err != nil {
		panic("docsmeta: default for " + key + " is not a valid int: " + err.Error())
	}
	return v
}

// DefaultBool returns the registered default parsed as a bool. "true" and "1"
// are true; everything else (including "") is false. Panics on an unregistered
// key.
func DefaultBool(key string) bool {
	v := DefaultFor(key)
	return v == "true" || v == "1"
}

// FieldDocs maps "StructName.FieldName" to a human-readable description for
// every exported field of the domain and storage-config structs surfaced in
// the generated reference. Internal/bookkeeping fields are documented here too
// (so the drift test finds a description for every exported field) and are also
// listed in InternalFields so the generator can omit them from user-facing
// output.
var FieldDocs = map[string]string{ // #nosec G101 -- values are human-readable docs for config fields; keys like "SFTPConfig.Password"/"S3Config.SecretKey" are field names surfaced in the manual, not credentials.
	// db.Job
	"Job.ID":                  "Unique identifier for the job.",
	"Job.Name":                "Human-readable name of the backup job.",
	"Job.Description":         "Optional free-text description of the job's purpose.",
	"Job.Enabled":             "Whether the scheduler runs this job automatically.",
	"Job.Schedule":            "Cron expression controlling when the job runs. Empty means manual-only.",
	"Job.BackupTypeChain":     "Backup mode for the job: full, incremental, or differential. Incremental and differential jobs automatically run as a full backup on their first run, when the job has no previous restore point to attach to.",
	"Job.RetentionCount":      "Number of most-recent restore points to keep. Ignored when LTR buckets are set.",
	"Job.RetentionDays":       "Age in days after which restore points are pruned. Ignored when LTR buckets are set.",
	"Job.Compression":         "Compression algorithm applied to the archive (e.g. zstd, gzip, none).",
	"Job.CompressionLevel":    "Compression level trading time for size (fastest, default, better, best). Empty means the algorithm's default.",
	"Job.Encryption":          "Encryption mode for the archive (e.g. none, passphrase).",
	"Job.ContainerMode":       "How Docker containers are handled during backup (e.g. stop, pause, hot).",
	"Job.VMMode":              "How libvirt VMs are handled during backup (e.g. shutdown, snapshot).",
	"Job.PreScript":           "Shell script run before the backup starts.",
	"Job.PostScript":          "Shell script run after the backup completes.",
	"Job.NotifyOn":            "When to send notifications for this job (\"always\", \"failure\", or \"never\").",
	"Job.VerifyBackup":        "Whether to verify the backup immediately after it is written.",
	"Job.StorageDestID":       "Foreign key to the storage destination this job writes to.",
	"Job.SourceID":            "Foreign key to the replication source, when applicable.",
	"Job.DeferRemoteUpload":   "When true, stage the archive locally and upload to remote storage asynchronously.",
	"Job.KeepLatest":          "Long-term retention: number of most-recent restore points to always keep.",
	"Job.KeepDaily":           "Long-term retention: number of daily restore points to keep.",
	"Job.KeepWeekly":          "Long-term retention: number of weekly restore points to keep.",
	"Job.KeepMonthly":         "Long-term retention: number of monthly restore points to keep.",
	"Job.KeepYearly":          "Long-term retention: number of yearly restore points to keep.",
	"Job.VerifySchedule":      "Cron expression for scheduled re-verification. Empty means none.",
	"Job.VerifyMode":          "Verification depth for scheduled verification (\"quick\" or \"deep\").",
	"Job.RetryMaxOverride":    "Per-job override for the maximum retry count. Null uses the global default.",
	"Job.RetryDelaysOverride": "Per-job override for the retry backoff schedule (JSON array of seconds). Null uses the global default.",
	"Job.AnomalySensitivity":  "Per-job anomaly-detection sensitivity override. Empty uses the global default.",
	"Job.MaxParallelUploads":  "Maximum concurrent upload workers for this job. 0 uses the default of 3.",
	"Job.CreatedAt":           "Timestamp when the job was created.",
	"Job.UpdatedAt":           "Timestamp when the job was last modified.",

	// db.StorageDestination
	"StorageDestination.ID":                    "Unique identifier for the storage destination.",
	"StorageDestination.Name":                  "Human-readable name of the storage destination.",
	"StorageDestination.Type":                  "Storage backend type (local, sftp, smb, nfs, webdav, s3).",
	"StorageDestination.Config":                "Backend-specific configuration stored as a JSON blob.",
	"StorageDestination.DedupEnabled":          "Whether content-addressed deduplication is enabled for this destination.",
	"StorageDestination.LastHealthCheckAt":     "Timestamp of the most recent health check.",
	"StorageDestination.LastHealthCheckStatus": "Result of the most recent health check.",
	"StorageDestination.LastHealthCheckError":  "Error message from the most recent failed health check.",
	"StorageDestination.ConsecutiveFailures":   "Count of consecutive health-check/storage failures, feeding the circuit breaker.",
	"StorageDestination.BreakerState":          "Circuit-breaker state (closed, open, half-open).",
	"StorageDestination.BreakerOpenedAt":       "Timestamp when the circuit breaker last opened.",
	"StorageDestination.BackupDatabaseEnabled": "Whether Vault's own database is backed up to this destination.",
	"StorageDestination.CapacityTotalBytes":    "Total capacity of the destination in bytes, if probed.",
	"StorageDestination.CapacityUsedBytes":     "Used capacity of the destination in bytes, if probed.",
	"StorageDestination.CapacityFreeBytes":     "Free capacity of the destination in bytes, if probed.",
	"StorageDestination.CapacityProbedAt":      "Timestamp of the most recent capacity probe. Null means never probed.",
	"StorageDestination.CapacitySource":        "How the capacity figures were obtained (e.g. statfs, api).",
	"StorageDestination.CapacityError":         "Error message from the most recent failed capacity probe.",
	"StorageDestination.AnomalySensitivity":    "Per-destination anomaly-detection sensitivity override. Empty uses the global default.",
	"StorageDestination.CreatedAt":             "Timestamp when the destination was created.",
	"StorageDestination.UpdatedAt":             "Timestamp when the destination was last modified.",

	// storage.SFTPConfig
	"SFTPConfig.Host":           "SFTP server hostname or IP address.",
	"SFTPConfig.Port":           "SFTP server port (default 22).",
	"SFTPConfig.User":           "Username for SFTP authentication.",
	"SFTPConfig.Password":       "Password for SFTP authentication. Leave empty when using a key file.",
	"SFTPConfig.KeyFile":        "Path to the private key file for key-based authentication.",
	"SFTPConfig.BasePath":       "Base directory on the server under which backups are stored.",
	"SFTPConfig.Path":           "Deprecated alias for BasePath, kept for backward compatibility.",
	"SFTPConfig.HostKey":        "SHA-256 fingerprint of the server's host public key for verification.",
	"SFTPConfig.KnownHostsFile": "Path to an OpenSSH known_hosts file used to verify the host key.",

	// storage.SMBConfig
	"SMBConfig.Host":     "SMB/CIFS server hostname or IP address.",
	"SMBConfig.Port":     "SMB server port (default 445).",
	"SMBConfig.User":     "Username for SMB authentication.",
	"SMBConfig.Password": "Password for SMB authentication.",
	"SMBConfig.Share":    "Name of the SMB share to connect to.",
	"SMBConfig.BasePath": "Base directory within the share under which backups are stored.",
	"SMBConfig.Path":     "Deprecated alias for BasePath, kept for backward compatibility.",

	// storage.NFSConfig
	"NFSConfig.Host":     "NFS server hostname or IP address.",
	"NFSConfig.Export":   "Remote export path, e.g. \"/mnt/user/backups\".",
	"NFSConfig.BasePath": "Sub-directory within the export under which backups are stored.",
	"NFSConfig.Version":  "NFS protocol version, \"3\" or \"4\" (default \"4\").",
	"NFSConfig.Options":  "Extra mount options, e.g. \"nolock,soft\".",

	// storage.WebDAVConfig
	"WebDAVConfig.URL":                 "Full WebDAV server endpoint, e.g. \"https://webdav.example.com/\".",
	"WebDAVConfig.Username":            "Username for WebDAV authentication. Optional for anonymous servers.",
	"WebDAVConfig.Password":            "Password for WebDAV authentication. Optional.",
	"WebDAVConfig.BasePath":            "Optional sub-directory under the server URL.",
	"WebDAVConfig.InsecureSkipVerify":  "Skip TLS certificate validation (for self-signed certificates).",
	"WebDAVConfig.TimeoutSeconds":      "Overall per-request lifetime cap in seconds. 0 means unlimited.",
	"WebDAVConfig.StallTimeoutSeconds": "Abort an upload if no bytes flow for this many seconds. Default 300; negative disables.",
	"WebDAVConfig.ChunkSizeMB":         "Chunk size in MiB for chunked uploads. 0 uses 50 MiB; negative disables chunking.",

	// storage.S3Config
	"S3Config.Bucket":               "Name of the S3 bucket.",
	"S3Config.Region":               "AWS region (or the region expected by an S3-compatible endpoint).",
	"S3Config.AccessKey":            "S3 access key ID.",
	"S3Config.SecretKey":            "S3 secret access key.",
	"S3Config.Endpoint":             "Optional custom endpoint for S3-compatible services (B2, MinIO, R2, Wasabi).",
	"S3Config.BasePath":             "Optional key prefix applied to all stored objects.",
	"S3Config.ForcePathStyle":       "Use path-style addressing instead of virtual-hosted style (for older S3-compatible services).",
	"S3Config.UploadTimeoutMinutes": "Per-object upload deadline in minutes. 0 uses the default of 240 (4 hours).",
	"S3Config.PartSizeMB":           "Multipart upload part size in MiB (valid 5-5120). 0 uses the default of 64.",

	// storage local config (inline struct in factory.go)
	"LocalConfig.Path": "Absolute path on the host filesystem where backups are stored.",
}

// InternalFields lists "StructName.FieldName" entries that are exported but not
// user-facing (identifiers, timestamps, and health/breaker/capacity bookkeeping
// columns). The generator omits these from the reference, and the drift test
// treats them as accounted-for.
var InternalFields = map[string]bool{
	// db.Job
	"Job.ID":            true,
	"Job.StorageDestID": true,
	"Job.SourceID":      true,
	"Job.CreatedAt":     true,
	"Job.UpdatedAt":     true,

	// db.StorageDestination
	"StorageDestination.ID":                    true,
	"StorageDestination.Config":                true,
	"StorageDestination.LastHealthCheckAt":     true,
	"StorageDestination.LastHealthCheckStatus": true,
	"StorageDestination.LastHealthCheckError":  true,
	"StorageDestination.ConsecutiveFailures":   true,
	"StorageDestination.BreakerState":          true,
	"StorageDestination.BreakerOpenedAt":       true,
	"StorageDestination.CapacityTotalBytes":    true,
	"StorageDestination.CapacityUsedBytes":     true,
	"StorageDestination.CapacityFreeBytes":     true,
	"StorageDestination.CapacityProbedAt":      true,
	"StorageDestination.CapacitySource":        true,
	"StorageDestination.CapacityError":         true,
	"StorageDestination.CreatedAt":             true,
	"StorageDestination.UpdatedAt":             true,
}

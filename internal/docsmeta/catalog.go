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
	{"history_retention_days", "int", "365", "Number of days to keep the history of job runs.", GroupGeneral},
	{"log_level", "string", "info", "Minimum log level for the daemon. The values are debug, info, warn, and error.", GroupGeneral},
	{"snapshot_path_override", "string", "", "Directory for temporary filesystem snapshots. An empty value uses the default directory.", GroupGeneral},
	{"staging_dir_override", "string", "", "Directory for temporary archives before upload. An empty value uses the default directory.", GroupGeneral},
	{"appdata_path", "string", "/mnt/user/appdata", "Path to the Unraid appdata share that contains the Docker container configurations.", GroupGeneral},
	{"storage_verbose_logging", "bool", "false", "If true, storage adapters log each storage operation.", GroupGeneral},
	{"replication_enabled", "string", "", "Set to true to enable replication scheduling. An empty value or false disables replication scheduling.", GroupGeneral},
	{"fuse_mount_base_dir", "string", "", "Directory for read-only FUSE backup mounts. An empty value uses /mnt/addons/vault-fuse. If /mnt/addons does not exist, Vault uses a temporary directory.", GroupGeneral},
	{"fuse_mount_idle_minutes", "int", "30", "Time in minutes before Vault unmounts an inactive FUSE backup mount.", GroupGeneral},
	{"memory_limit_mb", "int", "0", "Soft memory limit for the daemon, in MiB. Set to 0 to disable the limit. Other permitted values are 512 to 1048576. Set the limit to a value more than the memory necessary for the backup. Each S3 upload stream can use five times its part size. If memory use is more than the limit, the Go garbage collector runs more frequently. The GOMEMLIMIT environment variable overrides this setting.", GroupGeneral},

	// Backup engine
	{"adaptive_idle_cpu_percent", "int", "20", "CPU threshold for an adaptive backup. If CPU use is more than this percentage of one core, Vault treats the container or VM as active.", GroupBackup},
	{"adaptive_idle_net_kbps", "int", "500", "Network threshold for an adaptive backup, in kilobits per second. If the network rate is more than this value, Vault treats the container as active.", GroupBackup},
	{"adaptive_folder_idle_minutes", "int", "5", "Time in minutes for a folder in an adaptive backup. If a file in the folder changed in this time, Vault treats the folder as active.", GroupBackup},
	{"adaptive_recheck_minutes", "int", "2", "Time in minutes between checks for activity during a postponed backup.", GroupBackup},
	{"adaptive_max_postpone_minutes", "int", "60", "Maximum time in minutes to postpone an adaptive backup. Vault starts the backup when this time ends.", GroupBackup},
	{"auto_throttle_enabled", "bool", "false", "If true, Vault reduces its upload rate when other services use the internet connection. Set auto_throttle_link_mbps before you enable this setting.", GroupBackup},
	{"auto_throttle_link_mbps", "int", "0", "Upload capacity of the internet connection, in Mbps. Vault subtracts the upload rate of other services and reserves 10 percent of the capacity.", GroupBackup},
	{"auto_throttle_floor_mbps", "int", "5", "Minimum upload rate in Mbps that Vault can set when automatic throttling is on.", GroupBackup},
	{"retry_max_default", "int", "2", "Maximum number of times Vault retries a failed backup if the job has no retry limit.", GroupBackup},
	{"retry_delays_default", "json", "[900,3600,14400]", "Delay before each retry (JSON array in seconds). Vault uses these values if the job has no retry delays.", GroupBackup},
	{"breaker_fail_threshold", "int", "3", "Number of consecutive storage failures that open the circuit breaker for a destination.", GroupBackup},
	{"breaker_close_successes", "int", "2", "Number of consecutive successful operations necessary to close the circuit breaker.", GroupBackup},
	{"dedup_compaction_min_dead_ratio", "float", "0.5", "Minimum fraction of unreferenced blocks in a deduplication repository before compaction starts.", GroupBackup},
	{"global_exclude_paths", "json", "[]", "Paths to exclude from all folder and container backups (JSON array). Vault adds these paths to the exclusions for each item.", GroupBackup},
	{"label_exclusions_enabled", "bool", "true", "If true, Vault uses the comma-separated paths in a container's vault.exclude label as backup exclusions for that container.", GroupBackup},

	// Anomaly detection
	{"anomaly_detection_enabled", "bool", "true", "Set to true to enable anomaly detection for backup runs.", GroupAnomaly},
	{"anomaly_sensitivity_default", "string", "balanced", "Default sensitivity for anomaly detection. The values are strict, balanced, and permissive. A job or destination can set a different value.", GroupAnomaly},
	{"anomaly_notify_min_severity", "string", "critical", "Minimum anomaly severity for a notification. The values are info, warning, and critical.", GroupAnomaly},

	// Notifications
	{"notifications_enabled", "bool", "true", "Set to true to enable notifications.", GroupNotifications},
	{"discord_webhook_url", "string", "", "Discord webhook URL for notifications. An empty value disables Discord notifications.", GroupNotifications},
	{"discord_notify_on", "string", "always", "When Vault sends Discord notifications. The values are always and failure.", GroupNotifications},
	{"discord_bot_username", "string", "", "Optional user name for Discord webhook messages.", GroupNotifications},
	{"discord_bot_avatar_url", "string", "", "Optional URL for the image in Discord webhook messages.", GroupNotifications},
	{"discord_mention_role_id", "string", "", "ID of the Discord role to mention in notifications. An empty value disables role mentions.", GroupNotifications},
	{"discord_mention_on", "string", "never", "When Vault mentions the Discord role. The values are never, failure, and always.", GroupNotifications},

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
	"Job.ID":                    "Unique identifier for the job.",
	"Job.Name":                  "Name of the backup job.",
	"Job.Description":           "Optional text about the purpose of the job.",
	"Job.Enabled":               "If true, the scheduler starts the job at its scheduled times.",
	"Job.Schedule":              "Cron expression for the backup schedule. An empty value gives a manual backup job.",
	"Job.BackupTypeChain":       "Backup type (full, incremental, or differential). Vault makes a full backup on the first run of an incremental or differential job.",
	"Job.RetentionCount":        "Number of the latest restore points to keep. Vault ignores this value if Long-Term Retention is on.",
	"Job.RetentionDays":         "Age in days after which Vault removes a restore point. Vault ignores this value if Long-Term Retention is on.",
	"Job.Compression":           "Compression algorithm for the archive (for example, zstd, gzip, or none).",
	"Job.CompressionLevel":      "Compression level (fastest, default, better, or best). An empty value uses the default for the algorithm.",
	"Job.Encryption":            "Encryption mode for the archive (none or age).",
	"Job.ContainerMode":         "Container backup mode (one_by_one or stop_all).",
	"Job.VMMode":                "Virtual machine backup mode (snapshot or cold).",
	"Job.PreScript":             "Shell script that Vault runs before the backup starts.",
	"Job.PostScript":            "Shell script that Vault runs after the backup ends.",
	"Job.NotifyOn":              "When Vault sends notifications for this job. The values are always, failure, and never.",
	"Job.VerifyBackup":          "If true, Vault does an integrity check after it writes the backup.",
	"Job.StorageDestID":         "Foreign key to the storage destination this job writes to.",
	"Job.SourceID":              "Foreign key to the replication source, when applicable.",
	"Job.DeferRemoteUpload":     "If true, Vault puts the archive in local storage before it uploads the archive as a separate operation.",
	"Job.KeepLatest":            "Number of the latest restore points to keep with Long-Term Retention.",
	"Job.KeepDaily":             "Number of daily restore points to keep with Long-Term Retention.",
	"Job.KeepWeekly":            "Number of weekly restore points to keep with Long-Term Retention.",
	"Job.KeepMonthly":           "Number of monthly restore points to keep with Long-Term Retention.",
	"Job.KeepYearly":            "Number of yearly restore points to keep with Long-Term Retention.",
	"Job.VerifySchedule":        "Cron expression for scheduled verification. An empty value disables scheduled verification.",
	"Job.VerifyMode":            "Mode for scheduled verification (quick or deep).",
	"Job.FullBackupSchedule":    "Cron expression for full backups on an incremental or differential job. An empty value disables the separate schedule. Vault ignores this value for a full backup job.",
	"Job.RetryMaxOverride":      "Maximum number of times Vault retries this job. A null value uses the global default.",
	"Job.RetryDelaysOverride":   "Delay before each retry for this job (JSON array in seconds). A null value uses the global default.",
	"Job.AnomalySensitivity":    "Sensitivity for anomaly detection on this job. An empty value uses the global default.",
	"Job.MaxParallelUploads":    "Maximum number of uploads at the same time for this job. A value of 0 uses the default of 3.",
	"Job.AdaptiveEnabled":       "If true, Vault postpones a backup while a container, VM, or folder for the job is active. Vault checks again until the items become idle or the time limit ends. Global adaptive settings supply the limits.",
	"Job.AutoIncludeContainers": "If true, Vault adds new Docker containers to the job during a backup run. Vault skips containers with the vault.exclude label.",
	"Job.CreatedAt":             "Timestamp when the job was created.",
	"Job.UpdatedAt":             "Timestamp when the job was last modified.",

	// db.StorageDestination
	"StorageDestination.ID":                     "Unique identifier for the storage destination.",
	"StorageDestination.Name":                   "Name of the storage destination.",
	"StorageDestination.Type":                   "Storage backend type (local, sftp, smb, nfs, webdav, s3).",
	"StorageDestination.Config":                 "Backend-specific configuration stored as a JSON blob.",
	"StorageDestination.DedupEnabled":           "Whether content-addressed deduplication is enabled for this destination.",
	"StorageDestination.LastHealthCheckAt":      "Timestamp of the most recent health check.",
	"StorageDestination.LastHealthCheckStatus":  "Result of the most recent health check.",
	"StorageDestination.LastHealthCheckError":   "Error message from the most recent failed health check.",
	"StorageDestination.ConsecutiveFailures":    "Count of consecutive health-check/storage failures, feeding the circuit breaker.",
	"StorageDestination.BreakerState":           "Circuit-breaker state (closed, open, half-open).",
	"StorageDestination.BreakerOpenedAt":        "Timestamp when the circuit breaker last opened.",
	"StorageDestination.BackupDatabaseEnabled":  "If true, Vault backs up its database to this destination.",
	"StorageDestination.StageBesideDestination": "If true, classic backups put temporary archives next to this local destination.",
	"StorageDestination.CapacityTotalBytes":     "Total capacity of the destination in bytes, if probed.",
	"StorageDestination.CapacityUsedBytes":      "Used capacity of the destination in bytes, if probed.",
	"StorageDestination.CapacityFreeBytes":      "Free capacity of the destination in bytes, if probed.",
	"StorageDestination.CapacityProbedAt":       "Timestamp of the most recent capacity probe. Null means never probed.",
	"StorageDestination.CapacitySource":         "How the capacity figures were obtained (e.g. statfs, api).",
	"StorageDestination.CapacityError":          "Error message from the most recent failed capacity probe.",
	"StorageDestination.AnomalySensitivity":     "Sensitivity for anomaly detection on this destination. An empty value uses the global default.",
	"StorageDestination.CreatedAt":              "Timestamp when the destination was created.",
	"StorageDestination.UpdatedAt":              "Timestamp when the destination was last modified.",

	// storage.SFTPConfig
	"SFTPConfig.Host":           "Host name or IP address of the SFTP server.",
	"SFTPConfig.Port":           "SFTP server port (default 22).",
	"SFTPConfig.User":           "Username for SFTP authentication.",
	"SFTPConfig.Password":       "Password for SFTP authentication. Leave empty when using a key file.",
	"SFTPConfig.KeyFile":        "Path to the private key file for key-based authentication.",
	"SFTPConfig.BasePath":       "Directory on the server for the backups.",
	"SFTPConfig.Path":           "Old name for BasePath. Use BasePath for new configurations.",
	"SFTPConfig.HostKey":        "SHA-256 fingerprint of the server host key. If this field and known_hosts_file are empty, Vault records the first key it receives. Vault refuses a different key on later connections.",
	"SFTPConfig.KnownHostsFile": "Path to the OpenSSH known_hosts file. Vault uses this file to authenticate the server host key.",

	// storage.SMBConfig
	"SMBConfig.Host":     "Host name or IP address of the SMB server.",
	"SMBConfig.Port":     "SMB server port (default 445).",
	"SMBConfig.User":     "Username for SMB authentication.",
	"SMBConfig.Password": "Password for SMB authentication.",
	"SMBConfig.Share":    "Name of the SMB share.",
	"SMBConfig.BasePath": "Directory in the share for the backups.",
	"SMBConfig.Path":     "Old name for BasePath. Use BasePath for new configurations.",

	// storage.NFSConfig
	"NFSConfig.Host":     "Host name or IP address of the NFS server.",
	"NFSConfig.Export":   "Path of the export on the NFS server (for example, /mnt/user/backups).",
	"NFSConfig.BasePath": "Directory in the export for the backups.",
	"NFSConfig.Version":  "NFS protocol version (3 or 4). The default is 4.",
	"NFSConfig.Options":  "Additional mount options (for example, nolock,soft).",

	// storage.WebDAVConfig
	"WebDAVConfig.URL":                 "Full URL of the WebDAV server (for example, https://webdav.example.com/).",
	"WebDAVConfig.Username":            "Username for WebDAV authentication. Optional for anonymous servers.",
	"WebDAVConfig.Password":            "Password for WebDAV authentication. Optional.",
	"WebDAVConfig.BasePath":            "Optional directory under the server URL.",
	"WebDAVConfig.InsecureSkipVerify":  "If true, Vault does not validate the TLS certificate.",
	"WebDAVConfig.TimeoutSeconds":      "Time limit in seconds for each request. A value of 0 gives no total time limit.",
	"WebDAVConfig.StallTimeoutSeconds": "If no data moves for this time in seconds, Vault stops the upload. The default is 300 seconds. A negative value disables this limit.",
	"WebDAVConfig.ChunkSizeMB":         "Chunk size for uploads, in MiB. A value of 0 uses 50 MiB. A negative value disables chunking.",

	// storage.S3Config
	"S3Config.Bucket":               "Name of the S3 bucket.",
	"S3Config.Region":               "AWS region (or the region expected by an S3-compatible endpoint).",
	"S3Config.AccessKey":            "S3 access key ID.",
	"S3Config.SecretKey":            "S3 secret access key.",
	"S3Config.Endpoint":             "Optional URL of an S3-compatible service (for example, B2, MinIO, R2, or Wasabi).",
	"S3Config.BasePath":             "Optional prefix for the keys of all objects that Vault stores.",
	"S3Config.ForcePathStyle":       "If true, Vault uses path-style addressing. Some older S3-compatible services require this setting.",
	"S3Config.UploadTimeoutMinutes": "Time limit in minutes to upload one object. A value of 0 uses the default of 240 minutes.",
	"S3Config.PartSizeMB":           "Part size for multipart uploads, in MiB. The permitted range is 5 to 5120 MiB. A value of 0 uses the default of 64 MiB.",

	// storage local config (inline struct in factory.go)
	"LocalConfig.Path": "Absolute path on the host for the backups.",
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

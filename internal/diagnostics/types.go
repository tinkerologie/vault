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
type DiagnosticBundle struct {
	GeneratedAt   time.Time         `json:"generated_at"`
	CorrelationID string            `json:"correlation_id"`
	System        SystemInfo        `json:"system"`
	Database      DatabaseInfo      `json:"database"`
	Storage       []StorageInfo     `json:"storage"`
	Jobs          []JobInfo         `json:"jobs"`
	Runs          []RunInfo         `json:"runs"`
	Activity      []ActivityInfo    `json:"activity"`
	Runner        RunnerInfo        `json:"runner"`
	Replication   []ReplicationInfo `json:"replication"`
	Entries       []DiagnosticEntry `json:"entries"`
}

// SystemInfo holds environment metadata.
type SystemInfo struct {
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Hostname  string `json:"hostname"`
	Version   string `json:"vault_version"`
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
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Config string `json:"config"` // redacted JSON
}

// JobInfo holds job metadata with item counts.
type JobInfo struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Schedule  string `json:"schedule"`
	Enabled   bool   `json:"enabled"`
	ItemCount int    `json:"item_count"`
}

// RunInfo holds a summary of a job run.
type RunInfo struct {
	ID           int64      `json:"id"`
	JobID        int64      `json:"job_id"`
	Status       string     `json:"status"`
	RunType      string     `json:"run_type"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	ItemsTotal   int        `json:"items_total"`
	ItemsSuccess int        `json:"items_success"`
	ItemsFailed  int        `json:"items_failed"`
	ErrorMessage string     `json:"error_message,omitempty"`
}

// ActivityInfo holds an activity log entry.
type ActivityInfo struct {
	ID        int64     `json:"id"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
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

package storage

import "time"

// Capacity reports a destination's space accounting at the moment the
// probe ran. TotalBytes == 0 means "quota unknown" (S3 buckets, generic
// WebDAV servers that don't implement RFC 4331). Consumers render a
// percentage / progress bar ONLY when TotalBytes > 0; otherwise they
// show "Used: <UsedBytes>" alone.
//
// Source identifies which probe method produced the numbers so support
// reports can distinguish "statfs reported 320 GB" from "ListObjectsV2
// pagination summed 320 GB". Valid values: "statfs", "webdav-quota",
// "sftp-statvfs", "smb-fsctl", "s3-list-sum", "unknown".
type Capacity struct {
	TotalBytes int64     `json:"total_bytes"`
	UsedBytes  int64     `json:"used_bytes"`
	FreeBytes  int64     `json:"free_bytes"`
	ProbedAt   time.Time `json:"probed_at"`
	Source     string    `json:"source"`
}

// IsZero reports whether the Capacity has no useful information.
// Used by callers to decide whether to render a "never probed" state.
func (c Capacity) IsZero() bool {
	return c.TotalBytes == 0 && c.UsedBytes == 0 && c.FreeBytes == 0 && c.Source == "" && c.ProbedAt.IsZero()
}

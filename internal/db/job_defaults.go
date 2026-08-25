package db

// DefaultJob returns the field values a newly created Job takes when the
// caller does not supply them.
//
// This exists because three separate call sites used to hand-roll their own
// defaults and quietly disagreed: the REST create handler, the MCP create_job
// tool, and the replication import path. A Job created over MCP got a
// different backup chain and retention policy than the identical Job created
// over REST, which is invisible until the retention sweep deletes the wrong
// restore points.
//
// Callers start from this value and overwrite what the request specified.
// Zero-valued fields (Name, Schedule, StorageDestID, the LTR buckets, the
// retry overrides) are deliberately left zero: they have no sensible default
// and the DB column default or the caller supplies them.
func DefaultJob() Job {
	return Job{
		Enabled:         true,
		BackupTypeChain: "full",
		RetentionCount:  5,
		RetentionDays:   30,
		Compression:     "zstd",
		Encryption:      "none",
		ContainerMode:   "one_by_one",
		NotifyOn:        "failure",
		VerifyBackup:    true,
	}
}

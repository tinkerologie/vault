package diagnostics

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/logbuf"
	"github.com/ruaan-deysel/vault/internal/unraid"
)

// RunnerStatus holds a snapshot of the runner state for diagnostics.
type RunnerStatus struct {
	Active          bool
	JobID           int64
	JobName         string
	RunType         string
	ItemsTotal      int
	ItemsDone       int
	ItemsFailed     int
	CurrentItem     string
	CurrentItemType string
}

// StatusFunc returns a snapshot of the current runner status.
type StatusFunc func() RunnerStatus

// DedupStatsFunc fetches a dedup-repo stats snapshot for one
// destination. Supplied by the daemon (the diagnostics package can't
// import internal/runner without an import cycle); nil-safe — when
// nil the bundle simply omits per-destination dedup stats.
type DedupStatsFunc func(dest db.StorageDestination) (chunks, packs, logical, physical, wasted int64, lastGCAt time.Time, lastGCFreed int64, err error)

// NextRunFunc returns the next computed run time for a job ID as
// "YYYY-MM-DD HH:MM:SS" and an ok flag. Wired to scheduler.NextRun.
type NextRunFunc func(jobID int64) (string, bool)

// Collector gathers diagnostic data from the system.
type Collector struct {
	db        *db.DB
	statusFn  StatusFunc
	dedupFn   DedupStatsFunc
	nextRunFn NextRunFunc
	logRing   *logbuf.Ring
	version   string
	startTime time.Time
}

// NewCollector creates a new diagnostic collector. logRing may be nil
// (older daemons that haven't wired the ring buffer); when nil, the
// produced bundle simply omits the log tail.
func NewCollector(database *db.DB, statusFn StatusFunc, version string, logRing *logbuf.Ring) *Collector {
	return &Collector{
		db:        database,
		statusFn:  statusFn,
		logRing:   logRing,
		version:   version,
		startTime: time.Now(),
	}
}

// SetDedupStatsFunc wires a per-destination dedup-stats fetcher (the
// daemon owns the runner instance which owns the dedup repos). Safe to
// call after construction; nil disables per-destination dedup stats.
func (c *Collector) SetDedupStatsFunc(fn DedupStatsFunc) { c.dedupFn = fn }

// SetNextRunFunc wires a next-run resolver (scheduler.NextRun). Safe
// to call after construction.
func (c *Collector) SetNextRunFunc(fn NextRunFunc) { c.nextRunFn = fn }

// Collect gathers a complete diagnostic bundle from the system.
func (c *Collector) Collect() (*DiagnosticBundle, error) {
	correlationID := uuid.New().String()
	now := time.Now().UTC()
	hostname, _ := os.Hostname()

	bundle := &DiagnosticBundle{
		GeneratedAt:   now,
		CorrelationID: correlationID,
		System: SystemInfo{
			Version:   c.version,
			GoVersion: runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
			Hostname:  hostname,
			Disks:     c.collectDiskUsage(),
		},
	}

	var entries []DiagnosticEntry

	// Database info.
	bundle.Database = c.collectDatabaseInfo()
	entries = append(entries, infoEntry(now, correlationID, hostname, "database",
		"Database info collected",
		map[string]string{"path": bundle.Database.Path, "mode": bundle.Database.Mode}))

	// Settings dump — credential-free snapshot of operational config.
	bundle.Settings = c.collectSettings()
	entries = append(entries, infoEntry(now, correlationID, hostname, "settings",
		"Settings snapshot collected", nil))

	// Storage destinations.
	if dests, err := c.db.ListStorageDestinations(); err == nil {
		for _, d := range dests {
			si := StorageInfo{
				ID:                    d.ID,
				Name:                  d.Name,
				Type:                  d.Type,
				Config:                RedactJSON(d.Config),
				DedupEnabled:          d.DedupEnabled,
				LastHealthCheckAt:     d.LastHealthCheckAt,
				LastHealthCheckStatus: d.LastHealthCheckStatus,
				LastHealthCheckError:  d.LastHealthCheckError,
			}
			bundle.Storage = append(bundle.Storage, si)
		}
		entries = append(entries, infoEntry(now, correlationID, hostname, "storage",
			fmt.Sprintf("Found %d storage destination(s)", len(dests)), nil))
	} else {
		entries = append(entries, errEntry(now, correlationID, hostname, "storage",
			fmt.Sprintf("Failed to list storage destinations: %v", err)))
	}

	// Jobs + per-job items.
	if jobs, err := c.db.ListJobs(); err == nil {
		for _, j := range jobs {
			ji := JobInfo{
				ID:                j.ID,
				Name:              j.Name,
				Schedule:          j.Schedule,
				Enabled:           j.Enabled,
				BackupTypeChain:   j.BackupTypeChain,
				Compression:       j.Compression,
				HasEncryption:     strings.TrimSpace(j.Encryption) != "" && j.Encryption != "none",
				ContainerMode:     j.ContainerMode,
				VMMode:            j.VMMode,
				NotifyOn:          j.NotifyOn,
				VerifyBackup:      j.VerifyBackup,
				VerifySchedule:    j.VerifySchedule,
				VerifyMode:        j.VerifyMode,
				DeferRemoteUpload: j.DeferRemoteUpload,
				RetentionCount:    j.RetentionCount,
				RetentionDays:     j.RetentionDays,
				KeepLatest:        j.KeepLatest,
				KeepDaily:         j.KeepDaily,
				KeepWeekly:        j.KeepWeekly,
				KeepMonthly:       j.KeepMonthly,
				KeepYearly:        j.KeepYearly,
				StorageDestID:     j.StorageDestID,
			}
			if items, err := c.db.GetJobItems(j.ID); err == nil {
				ji.ItemCount = len(items)
				for _, it := range items {
					ji.Items = append(ji.Items, JobItemInfo{
						ID:       it.ID,
						ItemType: it.ItemType,
						ItemName: it.ItemName,
						ItemID:   it.ItemID,
						Settings: RedactJSON(it.Settings),
					})
				}
			}
			bundle.Jobs = append(bundle.Jobs, ji)
		}
		entries = append(entries, infoEntry(now, correlationID, hostname, "jobs",
			fmt.Sprintf("Found %d job(s)", len(jobs)), nil))
	} else {
		entries = append(entries, errEntry(now, correlationID, hostname, "jobs",
			fmt.Sprintf("Failed to list jobs: %v", err)))
	}

	// Recent runs (last 50 across all jobs).
	if runs, err := c.db.ListRecentRuns(50); err == nil {
		for _, r := range runs {
			ri := RunInfo{
				ID:           r.ID,
				JobID:        r.JobID,
				Status:       r.Status,
				BackupType:   r.BackupType,
				RunType:      r.RunType,
				StartedAt:    r.StartedAt,
				CompletedAt:  r.CompletedAt,
				ItemsTotal:   r.ItemsTotal,
				ItemsSuccess: r.ItemsDone,
				ItemsFailed:  r.ItemsFailed,
				SizeBytes:    r.SizeBytes,
				Log:          r.Log,
			}
			if r.CompletedAt != nil {
				ri.DurationSeconds = int(r.CompletedAt.Sub(r.StartedAt).Seconds())
			}
			if r.Status == "failed" {
				ri.ErrorMessages = extractRunErrors(r.Log)
			}
			bundle.Runs = append(bundle.Runs, ri)
		}
		entries = append(entries, infoEntry(now, correlationID, hostname, "runner",
			fmt.Sprintf("Collected %d recent run(s)", len(runs)), nil))
	} else {
		entries = append(entries, errEntry(now, correlationID, hostname, "runner",
			fmt.Sprintf("Failed to list recent runs: %v", err)))
	}

	// Activity logs (last 200).
	if logs, err := c.db.ListActivityLogs(200, ""); err == nil {
		for _, l := range logs {
			bundle.Activity = append(bundle.Activity, ActivityInfo{
				ID:        l.ID,
				Level:     l.Level,
				Category:  l.Category,
				Message:   l.Message,
				Details:   l.Details,
				CreatedAt: l.CreatedAt,
			})
		}
		entries = append(entries, infoEntry(now, correlationID, hostname, "activity",
			fmt.Sprintf("Collected %d activity log(s)", len(logs)), nil))
	} else {
		entries = append(entries, warnEntry(now, correlationID, hostname, "activity",
			fmt.Sprintf("Failed to list activity logs: %v", err)))
	}

	// Runner status.
	if c.statusFn != nil {
		s := c.statusFn()
		bundle.Runner = RunnerInfo(s)
	}

	// Replication sources.
	if sources, err := c.db.ListReplicationSources(); err == nil {
		for _, s := range sources {
			bundle.Replication = append(bundle.Replication, ReplicationInfo{
				ID:       s.ID,
				Name:     s.Name,
				URL:      RedactURL(s.URL),
				Enabled:  s.Enabled,
				Interval: s.Schedule,
			})
		}
		entries = append(entries, infoEntry(now, correlationID, hostname, "replication",
			fmt.Sprintf("Found %d replication source(s)", len(sources)), nil))
	} else {
		entries = append(entries, warnEntry(now, correlationID, hostname, "replication",
			fmt.Sprintf("Failed to list replication sources: %v", err)))
	}

	// Daemon log tail. Always redacted before embedding.
	if c.logRing != nil {
		snap := c.logRing.Snapshot()
		if len(snap) > 0 {
			bundle.LogTail = string(RedactLogLines(snap))
			entries = append(entries, infoEntry(now, correlationID, hostname, "logbuf",
				fmt.Sprintf("Captured %d byte(s) of daemon log", len(bundle.LogTail)), nil))
		}
	}

	// PR2 extensions — extended troubleshooting context.
	bundle.Runtime = c.collectRuntime()
	bundle.Pools = c.collectPools()
	bundle.Connectivity = c.collectConnectivity()
	bundle.VerifyRuns = c.collectVerifyRuns(&entries, now, correlationID, hostname)
	bundle.DedupStats = c.collectDedupStats(bundle.Storage, &entries, now, correlationID, hostname)
	bundle.Scheduler = c.collectScheduler(bundle.Jobs)

	bundle.Entries = entries
	return bundle, nil
}

// collectRuntime snapshots Go runtime metrics for memory-leak and
// goroutine-pileup investigations.
func (c *Collector) collectRuntime() RuntimeInfo {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return RuntimeInfo{
		NumGoroutine:    runtime.NumGoroutine(),
		NumCPU:          runtime.NumCPU(),
		NumGC:           ms.NumGC,
		HeapAllocBytes:  ms.HeapAlloc,
		HeapSysBytes:    ms.HeapSys,
		HeapObjects:     ms.HeapObjects,
		StackInUseBytes: ms.StackInuse,
		UptimeSeconds:   int64(time.Since(c.startTime).Seconds()),
	}
}

// collectPools enumerates Unraid pools and their mount state. Targets
// the #69 class of "cache pool not detected" reports.
func (c *Collector) collectPools() []PoolInfo {
	pools := unraid.DiscoverPools()
	out := make([]PoolInfo, 0, len(pools))
	for _, p := range pools {
		out = append(out, PoolInfo{Path: p, Mounted: unraid.IsMountedPool(p)})
	}
	return out
}

// collectConnectivity probes the Docker and libvirt control planes.
// Errors are captured (not raised) so the bundle still completes when
// either is unavailable — that absence is itself the diagnostic.
func (c *Collector) collectConnectivity() ConnectivityInfo {
	conn := ConnectivityInfo{}

	// Docker.
	if ch, err := engine.NewContainerHandler(); err != nil {
		conn.Docker.Error = err.Error()
	} else {
		conn.Docker.Available = true
		if items, listErr := ch.ListItems(); listErr != nil {
			conn.Docker.Error = listErr.Error()
		} else {
			conn.Docker.ContainerCount = len(items)
		}
	}

	// libvirt — NewVMHandler returns the stub error on non-Linux.
	if vh, err := engine.NewVMHandler(); err != nil { //nolint:staticcheck // platform-dependent: stub returns error on non-Linux
		conn.Libvirt.Error = err.Error()
	} else {
		conn.Libvirt.Available = true
		if items, listErr := vh.ListItems(); listErr != nil { //nolint:staticcheck
			conn.Libvirt.Error = listErr.Error()
		} else {
			conn.Libvirt.VMCount = len(items)
		}
	}

	return conn
}

// collectVerifyRuns pulls the last 25 verify-runs across all restore
// points. Critical signal for "my weekly verify keeps failing" reports.
func (c *Collector) collectVerifyRuns(entries *[]DiagnosticEntry, now time.Time, cid, host string) []VerifyRunInfo {
	runs, err := c.db.ListRecentVerifyRuns(25)
	if err != nil {
		*entries = append(*entries, warnEntry(now, cid, host, "verify",
			fmt.Sprintf("Failed to list recent verify runs: %v", err)))
		return nil
	}
	out := make([]VerifyRunInfo, 0, len(runs))
	for _, r := range runs {
		out = append(out, VerifyRunInfo{
			ID:             r.ID,
			RestorePointID: r.RestorePointID,
			Mode:           r.Mode,
			Status:         r.Status,
			FilesChecked:   r.FilesChecked,
			FilesFailed:    r.FilesFailed,
			BytesRead:      r.BytesRead,
			StartedAt:      r.StartedAt,
			CompletedAt:    r.CompletedAt,
			ErrorSummary:   r.ErrorSummary,
		})
	}
	*entries = append(*entries, infoEntry(now, cid, host, "verify",
		fmt.Sprintf("Collected %d verify run(s)", len(out)), nil))
	return out
}

// collectDedupStats fetches per-destination dedup stats for every
// dedup-enabled storage destination already collected. Skipped
// entirely when the daemon hasn't wired a DedupStatsFunc (avoids an
// import cycle on internal/runner).
func (c *Collector) collectDedupStats(storage []StorageInfo, entries *[]DiagnosticEntry, now time.Time, cid, host string) []DedupStatsInfo {
	if c.dedupFn == nil {
		return nil
	}
	dests, err := c.db.ListStorageDestinations()
	if err != nil {
		*entries = append(*entries, warnEntry(now, cid, host, "dedup",
			fmt.Sprintf("Failed to re-list destinations for dedup stats: %v", err)))
		return nil
	}
	out := make([]DedupStatsInfo, 0)
	for _, d := range dests {
		if !d.DedupEnabled {
			continue
		}
		chunks, packs, logical, physical, wasted, lastGCAt, lastGCFreed, sErr := c.dedupFn(d)
		ds := DedupStatsInfo{
			StorageID:           d.ID,
			StorageName:         d.Name,
			TotalChunks:         chunks,
			TotalPacks:          packs,
			LogicalBytes:        logical,
			PhysicalBytes:       physical,
			WastedBytesEstimate: wasted,
			LastGCAt:            lastGCAt,
			LastGCFreedBytes:    lastGCFreed,
		}
		if physical > 0 {
			ds.DedupRatio = float64(logical) / float64(physical)
		} else {
			ds.DedupRatio = 1.0
		}
		if sErr != nil {
			ds.Error = sErr.Error()
		}
		out = append(out, ds)
	}
	_ = storage // signature retained for symmetry with other collect* helpers
	if len(out) > 0 {
		*entries = append(*entries, infoEntry(now, cid, host, "dedup",
			fmt.Sprintf("Collected dedup stats for %d destination(s)", len(out)), nil))
	}
	return out
}

// collectScheduler resolves next-run timestamps per job from the
// daemon-supplied resolver. No-op when the resolver isn't wired.
func (c *Collector) collectScheduler(jobs []JobInfo) SchedulerInfo {
	if c.nextRunFn == nil {
		return SchedulerInfo{}
	}
	out := SchedulerInfo{}
	for _, j := range jobs {
		if !j.Enabled {
			continue
		}
		nr := NextRunInfo{JobID: j.ID, JobName: j.Name}
		if t, ok := c.nextRunFn(j.ID); ok {
			nr.NextRun = t
		}
		out.NextRuns = append(out.NextRuns, nr)
		if j.VerifySchedule != "" {
			// Verify entries are keyed separately in the scheduler;
			// without a dedicated resolver we surface the cron string
			// itself so support can sanity-check the configured
			// cadence. Future work: extend scheduler.NextRun to take
			// a kind ("backup"/"verify") parameter and report both.
			out.NextVerifyRuns = append(out.NextVerifyRuns, NextRunInfo{
				JobID: j.ID, JobName: j.Name, NextRun: "schedule=" + j.VerifySchedule,
			})
		}
	}
	return out
}

// collectDatabaseInfo gathers database file information.
func (c *Collector) collectDatabaseInfo() DatabaseInfo {
	info := DatabaseInfo{
		Path: c.db.Path(),
		Mode: "legacy_usb",
	}

	// Detect hybrid mode by checking for a snapshot path setting.
	if override, err := c.db.GetSetting("snapshot_path_override", ""); err == nil && override != "" {
		info.Mode = "hybrid"
	} else if pool := unraid.PreferredPool(); pool != "" && unraid.IsMountedPool(pool) {
		info.Mode = "hybrid"
	}

	if fi, err := os.Stat(c.db.Path()); err == nil {
		info.SizeBytes = fi.Size()
	}

	return info
}

// collectSettings extracts an operationally-interesting subset of the
// settings table with all credentials reduced to existence booleans.
func (c *Collector) collectSettings() SettingsInfo {
	get := func(k, def string) string {
		v, err := c.db.GetSetting(k, def)
		if err != nil {
			return def
		}
		return v
	}
	hasNonempty := func(k string) bool { return strings.TrimSpace(get(k, "")) != "" }
	asBool := func(k string, def bool) bool {
		v := get(k, "")
		if v == "" {
			return def
		}
		// Settings store stringified bools — match the convention used
		// elsewhere in the codebase ("false" disables the feature).
		return v != "false" && v != "0" && v != "off"
	}
	return SettingsInfo{
		EncryptionConfigured:   hasNonempty("encryption_passphrase") || hasNonempty("encryption_passphrase_sealed"),
		APIKeyConfigured:       hasNonempty("api_key_hash"),
		StagingDirOverride:     get("staging_dir_override", ""),
		SnapshotPathOverride:   get("snapshot_path_override", ""),
		ContainerBackupEnabled: asBool("container_backup_enabled", true),
		VMBackupEnabled:        asBool("vm_backup_enabled", true),
		FlashBackupEnabled:     asBool("flash_backup_enabled", true),
		TimeFormat:             get("time_format", ""),
		NotificationProvider:   get("notification_provider", ""),
	}
}

// collectDiskUsage probes the paths Vault depends on. Stat failures
// produce an entry with Error set so support tickets can tell "not
// mounted / wrong path" apart from "100 % full".
func (c *Collector) collectDiskUsage() []DiskUsage {
	// Build the candidate set lazily from settings so the snapshot
	// reflects the actual staging dir, not a guess.
	paths := []string{"/boot", "/tmp", "/var/local/vault"}
	if dbPath := c.db.Path(); dbPath != "" {
		paths = append(paths, dbDir(dbPath))
	}
	if staging, err := c.db.GetSetting("staging_dir_override", ""); err == nil && staging != "" {
		paths = append(paths, staging)
	}
	if snap, err := c.db.GetSetting("snapshot_path_override", ""); err == nil && snap != "" {
		paths = append(paths, dbDir(snap))
	}
	paths = append(paths, unraid.DiscoverPools()...)

	seen := make(map[string]bool, len(paths))
	out := make([]DiskUsage, 0, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, probeDisk(p))
	}
	return out
}

// probeDisk runs statfs on path and returns capacity, free, and a
// crude used percentage. Path-missing and permission-denied errors are
// captured as the entry's Error field rather than swallowed so support
// reports show the discrepancy.
func probeDisk(path string) DiskUsage {
	d := DiskUsage{Path: path}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		d.Error = err.Error()
		return d
	}
	d.TotalBytes = stat.Blocks * uint64(stat.Bsize) //nolint:gosec // bsize is non-negative
	d.FreeBytes = stat.Bavail * uint64(stat.Bsize)  //nolint:gosec // bsize is non-negative
	if d.TotalBytes > 0 {
		used := d.TotalBytes - d.FreeBytes
		d.UsedPct = int((used * 100) / d.TotalBytes)
	}
	return d
}

// dbDir returns the directory containing a database file path.
func dbDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return path
}

// extractRunErrors pulls error strings out of the run.log column, which
// is a JSON array of per-item objects. Returns nil if the log isn't
// valid JSON (older runs may have plain-text logs). Used to surface
// failure messages without forcing operators to grep through the raw
// JSON.
func extractRunErrors(logJSON string) []string {
	if logJSON == "" {
		return nil
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(logJSON), &items); err != nil {
		return nil
	}
	var errs []string
	for _, it := range items {
		if msg, ok := it["error"].(string); ok && msg != "" {
			name, _ := it["name"].(string)
			if name != "" {
				errs = append(errs, fmt.Sprintf("%s: %s", name, msg))
			} else {
				errs = append(errs, msg)
			}
		}
	}
	return errs
}

func infoEntry(t time.Time, cid, host, service, msg string, ctx map[string]string) DiagnosticEntry {
	return DiagnosticEntry{
		Timestamp: t, Level: LevelInfo, CorrelationID: cid,
		Service: service, Host: host, Message: msg, Context: ctx,
	}
}

func warnEntry(t time.Time, cid, host, service, msg string) DiagnosticEntry {
	return DiagnosticEntry{
		Timestamp: t, Level: LevelWarn, CorrelationID: cid,
		Service: service, Host: host, Message: msg,
	}
}

func errEntry(t time.Time, cid, host, service, msg string) DiagnosticEntry {
	return DiagnosticEntry{
		Timestamp: t, Level: LevelError, CorrelationID: cid,
		Service: service, Host: host, Message: msg,
	}
}

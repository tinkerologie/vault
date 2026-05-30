package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ruaan-deysel/vault/internal/anomaly"
	"github.com/ruaan-deysel/vault/internal/api"
	"github.com/ruaan-deysel/vault/internal/api/handlers"
	"github.com/ruaan-deysel/vault/internal/config"
	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/diagnostics"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/logbuf"
	"github.com/ruaan-deysel/vault/internal/replication"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/scheduler"
	"github.com/ruaan-deysel/vault/internal/tempdir"
	"github.com/ruaan-deysel/vault/internal/unraid"
	"github.com/spf13/cobra"
)

// daemonLogBufferBytes caps the in-memory log ring used by the
// diagnostics bundle. 1 MiB ≈ the last few thousand log lines — enough
// to cover a typical multi-job nightly run end-to-end while staying
// well under the daemon's RSS budget. Sized once at package level so
// tests can override via the unexported var if needed.
const daemonLogBufferBytes = 1 << 20

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the Vault daemon (API server + scheduler)",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")
		addr, _ := cmd.Flags().GetString("addr")
		tlsCert, _ := cmd.Flags().GetString("tls-cert")
		tlsKey, _ := cmd.Flags().GetString("tls-key")

		// Tee the standard logger into an in-memory ring buffer so the
		// diagnostics bundle can include the last ~1 MiB of daemon log
		// output. Wired here at the very top of RunE so every
		// subsequent log.Printf is captured, including pool-discovery
		// and DB-restoration messages that are critical for support
		// reports. The ring buffer write path is always-nil-safe and
		// the original stderr destination is preserved.
		logRing := logbuf.New(daemonLogBufferBytes)
		log.SetOutput(io.MultiWriter(os.Stderr, logRing))

		// Hybrid mode detection: if a pool drive is mounted, use a RAM-backed
		// working database with periodic snapshots to the pool drive. This
		// avoids USB flash wear from SQLite WAL writes.
		const usbBackupPath = "/boot/config/plugins/vault/vault.db.backup"

		var (
			hybridMode      bool
			snapshotPath    string
			snapshotMgr     *db.SnapshotManager
			actualDBPath    = dbPath
			detectedPool    string
			defaultSnapPath string
		)

		// Determine the vault.cfg path (same directory as the DB flag).
		cfgPath := filepath.Join(filepath.Dir(dbPath), "vault.cfg")

		// Pool detection with retry — Unraid's boot sequence can launch
		// plugins before the array/pool drives are fully mounted. Without
		// a retry window, the daemon falls through to boot-device-direct
		// mode for its entire lifetime (and on the first start after a
		// hybrid-migrated install, the primary DB has already been
		// renamed to vault.db.backup, so a fresh empty schema is created
		// and configuration appears lost — issue #108). Wait up to 30 s
		// for a pool to appear mounted before giving up.
		const (
			poolDetectionMaxAttempts = 15
			poolDetectionInterval    = 2 * time.Second
		)
		poolRetryStart := time.Now()
		for attempt := 1; attempt <= poolDetectionMaxAttempts; attempt++ {
			detectedPool = unraid.PreferredPool()
			if detectedPool != "" && unraid.IsMountedPool(detectedPool) {
				if attempt > 1 {
					log.Printf("Pool %s became available after %v (attempt %d/%d)",
						detectedPool,
						time.Duration(attempt-1)*poolDetectionInterval,
						attempt, poolDetectionMaxAttempts)
				}
				break
			}
			if attempt == 1 {
				log.Printf("No mounted pool detected yet, waiting up to %v for array startup...",
					time.Duration(poolDetectionMaxAttempts)*poolDetectionInterval)
			}
			if attempt < poolDetectionMaxAttempts {
				time.Sleep(poolDetectionInterval)
			}
		}
		poolRetryWait := time.Since(poolRetryStart)

		// Log final discovered-pools state — helps diagnose "No cache
		// drive detected" reports where a pool exists under /mnt/ but
		// isn't mounted (issue #69).
		if pools := unraid.DiscoverPools(); len(pools) > 0 {
			for _, p := range pools {
				if unraid.IsMountedPool(p) {
					log.Printf("Discovered pool: %s (mounted)", p)
				} else {
					log.Printf("Discovered pool: %s (NOT mounted)", p)
				}
			}
		}
		if detectedPool != "" {
			defaultSnapPath = filepath.Join(detectedPool, ".vault", "vault.db")
			cacheState := checkCacheMount(detectedPool)
			switch cacheState {
			case cacheMounted:
				hybridMode = true
				log.Printf("Pool drive detected and mounted at %s", detectedPool)
			case cacheEmptyNotMounted:
				log.Printf("Warning: %s exists but appears unmounted — the array may not be started yet", detectedPool)
				log.Println("Warning: falling back to USB-direct mode with degraded persistence")
			case cacheNotExist:
				log.Printf("Warning: pool %s not accessible — database writes go directly to USB flash (increased wear)", detectedPool)
			}
		} else {
			log.Println("Warning: no pool drive detected — database writes go directly to USB flash (increased wear)")
		}

		if hybridMode {
			workingDir := "/var/local/vault"
			if err := os.MkdirAll(workingDir, 0o750); err != nil {
				return fmt.Errorf("creating hybrid working directory: %w", err)
			}
			actualDBPath = filepath.Join(workingDir, "vault.db")

			// Read the snapshot path from vault.cfg BEFORE any restoration.
			// This resolves the chicken-and-egg problem where the snapshot_path_override
			// setting is stored inside the database that hasn't been restored yet.
			snapshotPath = defaultSnapPath
			if cfgSnapPath := config.ReadCfgValue(cfgPath, "SNAPSHOT_PATH", ""); cfgSnapPath != "" {
				snapshotPath = cfgSnapPath
				log.Printf("Using snapshot path from vault.cfg: %s", snapshotPath)
			}

			// First-run migration: if the USB DB exists and no snapshot exists
			// yet, copy the USB DB to the snapshot path so it becomes the seed.
			if _, usbErr := os.Stat(dbPath); usbErr == nil {
				if _, snapErr := os.Stat(snapshotPath); os.IsNotExist(snapErr) {
					log.Printf("Migrating USB database %s → snapshot %s", dbPath, snapshotPath)
					if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o750); err != nil {
						return fmt.Errorf("creating snapshot directory: %w", err)
					}
					tmpDB, err := db.Open(dbPath)
					if err != nil {
						return fmt.Errorf("opening USB database for migration: %w", err)
					}
					migrator := db.NewSnapshotManager(tmpDB, snapshotPath, defaultSnapPath)
					if err := migrator.SaveSnapshot(); err != nil {
						_ = tmpDB.Close()
						return fmt.Errorf("migrating USB database to snapshot: %w", err)
					}
					_ = tmpDB.Close()

					// Preserve the USB DB as a backup instead of deleting it.
					// This provides a fallback if the cache drive is unavailable on next boot.
					if err := os.Rename(dbPath, usbBackupPath); err != nil {
						log.Printf("Warning: failed to preserve USB database as backup: %v", err)
						// Fall back to removal if rename fails.
						if rmErr := os.Remove(dbPath); rmErr != nil {
							log.Printf("Warning: failed to remove migrated USB database: %v", rmErr)
						}
					} else {
						log.Printf("USB database migrated and preserved as backup: %s", usbBackupPath)
					}
					_ = os.Remove(dbPath + ".migrated")
				}
			}

			log.Printf("Hybrid mode: working DB at %s, snapshots at %s", actualDBPath, snapshotPath)
		} else {
			// Non-hybrid (USB-direct) restoration fallback. If the
			// primary DB at /boot/.../vault.db is missing — typical
			// after a hybrid-mode first-run migration renamed it to
			// vault.db.backup, then the pool drive failed to mount on
			// a later boot (e.g. Unraid upgrade restart) — but the USB
			// safety-net backup exists, copy it back into place BEFORE
			// db.Open creates a fresh empty schema. Without this step,
			// db.Open would silently produce a brand-new empty database
			// and every saved job, destination, and setting would be
			// invisible to the running daemon, exactly matching the
			// "configuration lost after Unraid upgrade/restart" symptom
			// (issue #108).
			primaryExists := false
			if fi, err := os.Stat(actualDBPath); err == nil && fi.Size() > 0 {
				primaryExists = true
			}
			backupExists := false
			var backupSize int64
			if fi, err := os.Stat(usbBackupPath); err == nil && fi.Size() > 0 {
				backupExists = true
				backupSize = fi.Size()
			}
			if !primaryExists && backupExists {
				log.Printf("Non-hybrid mode: primary DB %s missing or empty; restoring from USB backup %s (%d bytes)",
					actualDBPath, usbBackupPath, backupSize)
				if err := copyFile(usbBackupPath, actualDBPath); err != nil {
					log.Printf("Warning: USB backup restore failed: %v — daemon will start with a fresh database", err)
				} else {
					log.Printf("Configuration restored from USB backup at %s", usbBackupPath)
				}
			} else if !primaryExists && !backupExists {
				log.Printf("Warning: neither primary DB nor USB backup found — daemon will start with a fresh database (configuration will need to be reconfigured)")
			}
		}

		database, err := db.Open(actualDBPath)
		if err != nil {
			return err
		}
		defer func() { _ = database.Close() }() // #nosec G104 — best-effort close at shutdown

		// In hybrid mode, attempt restoration using a fallback chain.
		if hybridMode {
			snapshotMgr = db.NewSnapshotManager(database, snapshotPath, defaultSnapPath)
			restorationInfo := restoreWithFallback(snapshotMgr, snapshotPath, defaultSnapPath, usbBackupPath)
			snapshotMgr.SetRestorationInfo(restorationInfo)
			log.Printf("Database restoration: source=%s path=%s (%s)",
				restorationInfo.Source, restorationInfo.Path, restorationInfo.Reason)

			_ = database.Close()
			database, err = db.Open(actualDBPath)
			if err != nil {
				return fmt.Errorf("re-opening database after snapshot restore: %w", err)
			}

			// Re-create the snapshot manager with the final DB handle.
			// vault.cfg is the sole authority for the snapshot path — no
			// sync-back from the restored DB's snapshot_path_override setting.
			// The running SetSnapshotPath handler writes both DB and vault.cfg
			// atomically and saves a fresh snapshot at the new location, so
			// existing snapshots are always consistent with vault.cfg.
			snapshotMgr = db.NewSnapshotManager(database, snapshotPath, defaultSnapPath)
			snapshotMgr.SetUSBBackupPath(usbBackupPath)
			snapshotMgr.SetRestorationInfo(restorationInfo)

			// Refresh the USB safety-net immediately after a successful
			// restoration. The USB backup is normally only written on
			// graceful shutdown / config mutation; if a previous daemon was
			// killed (e.g. an old plugin upgrade that didn't stop the
			// service), the USB backup can be missing or stale. Forcing a
			// flush here guarantees that even an unclean shutdown later
			// leaves a current recovery point on the USB flash, so the next
			// upgrade can never silently fall through to a fresh database
			// (issue #74).
			if restorationInfo != nil && restorationInfo.Source != "fresh" {
				if err := snapshotMgr.FlushToUSB(); err != nil {
					log.Printf("Warning: initial USB backup flush after restoration failed: %v", err)
				} else {
					log.Printf("Refreshed USB backup at %s after restoration from %s",
						usbBackupPath, restorationInfo.Source)
				}
			}
		}

		// Validate that the database contains operator configuration
		// (≥1 job or ≥1 storage destination). If not, log a prominent
		// warning so an empty start after a failed restoration is
		// obvious in the support log and via /api/v1/health (issue #108).
		var configSummary *db.ConfigurationSummary
		if summary, validateErr := database.ValidateHasConfiguration(context.Background()); validateErr != nil {
			log.Printf("Warning: failed to validate database configuration: %v", validateErr)
		} else {
			configSummary = summary
			if summary.HasConfiguration {
				log.Printf("Database configuration validated: %d jobs, %d storage destinations, %d settings",
					summary.Jobs, summary.StorageDests, summary.Settings)
			} else {
				log.Printf("WARNING: database contains no jobs or storage destinations — this is normal for a fresh install, but unexpected after an upgrade/restart. Fallback paths attempted: snapshot=%q usb_backup=%s. If you expected your previous configuration, see https://github.com/ruaan-deysel/vault/issues/108",
					snapshotPath, usbBackupPath)
			}
		}

		// Build startup diagnostics snapshot exposed via /api/v1/health
		// so `make verify`, the Settings page, and support tooling can
		// confirm persistence end-to-end without parsing the daemon log.
		startupDiag := &api.StartupDiagnostics{
			HybridMode:      hybridMode,
			DetectedPool:    detectedPool,
			PoolMounted:     detectedPool != "" && unraid.IsMountedPool(detectedPool),
			PoolRetryWaitMs: poolRetryWait.Milliseconds(),
			UnraidBootKind:  detectBootKind(),
			Configuration:   configSummary,
		}
		if configSummary != nil {
			startupDiag.HasConfiguration = configSummary.HasConfiguration
		}
		if snapshotMgr != nil {
			startupDiag.RestorationInfo = snapshotMgr.RestorationSource()
		}
		log.Printf("Startup diagnostics: hybrid=%v pool=%q pool_mounted=%v wait=%v boot_kind=%s has_config=%v",
			startupDiag.HybridMode, startupDiag.DetectedPool, startupDiag.PoolMounted,
			poolRetryWait.Round(time.Millisecond), startupDiag.UnraidBootKind, startupDiag.HasConfiguration)

		// Prune activity log entries older than 90 days on startup.
		if err := database.DeleteOldActivityLogs(90); err != nil {
			log.Printf("Warning: failed to prune old activity logs: %v", err)
		}

		// Remove failed/error job runs older than 30 days.
		if cleaned, err := database.DeleteOldFailedRuns(30); err != nil {
			log.Printf("Warning: failed to prune old failed runs: %v", err)
		} else if cleaned > 0 {
			log.Printf("Pruned %d old failed job run(s)", cleaned)
		}

		// Mark any "running" job runs as failed — they were interrupted by
		// a previous daemon crash or restart.
		if cleaned, err := database.CleanupStaleRuns(); err != nil {
			log.Printf("Warning: failed to clean up stale job runs: %v", err)
		} else if cleaned > 0 {
			log.Printf("Cleaned up %d stale job run(s) from previous session", cleaned)
		}

		// Cap activity log to 10,000 rows to prevent unbounded growth.
		if err := database.CapActivityLogs(10000); err != nil {
			log.Printf("Warning: failed to cap activity logs: %v", err)
		}

		// Reclaim disk space after all cleanup operations.
		if err := database.Vacuum(); err != nil {
			log.Printf("Warning: failed to vacuum database: %v", err)
		}

		// Clean up staging directories left behind by crashed backup/restore runs.
		if dests, err := database.ListStorageDestinations(); err == nil {
			configs := make([]tempdir.StorageConfig, len(dests))
			for i, d := range dests {
				configs[i] = tempdir.StorageConfig{Type: d.Type, Config: d.Config}
			}
			tempdir.CleanupStale(configs)
		} else {
			log.Printf("Warning: failed to list storage destinations for cleanup: %v", err)
		}

		// Load or generate the server key for sealing secrets at rest.
		// Stored alongside the database file.
		keyPath := filepath.Join(filepath.Dir(dbPath), "vault.key")
		serverKey, err := crypto.LoadOrCreateServerKey(keyPath)
		if err != nil {
			return fmt.Errorf("loading server key: %w", err)
		}

		// Migrate any legacy plaintext encryption passphrase to sealed form.
		if plaintext, _ := database.GetSetting("encryption_passphrase", ""); plaintext != "" {
			sealed, sealErr := crypto.Seal(serverKey, plaintext)
			if sealErr != nil {
				log.Printf("Warning: failed to seal legacy passphrase: %v", sealErr)
			} else {
				_ = database.SetSetting("encryption_passphrase_sealed", sealed)
				_ = database.SetSetting("encryption_passphrase", "")
				log.Println("Migrated legacy plaintext passphrase to sealed storage")
			}
		}

		log.Println("Starting Vault daemon...")

		cfg := api.ServerConfig{
			Addr:      addr,
			TLSCert:   tlsCert,
			TLSKey:    tlsKey,
			ServerKey: serverKey,
			Version:   version,
		}
		srv := api.NewServer(database, cfg)
		srv.SetStartupDiagnostics(startupDiag)

		// Discover NVMe-backed ZFS pools and wire them into the staging
		// cascade and browse handler for ZFS-aware path browsing.
		zfsH, zfsErr := engine.NewZFSHandler()
		if zfsErr == nil {
			// Prepend NVMe pool mountpoints to the staging cascade so they
			// are preferred over conventional Unraid pools.
			if nvmePools, err := zfsH.ListNVMePools(); err == nil && len(nvmePools) > 0 {
				var paths []string
				for _, p := range nvmePools {
					paths = append(paths, p.Mountpoint)
					log.Printf("NVMe ZFS pool detected: %s → cache base %s", p.Name, p.Mountpoint)
				}
				tempdir.PrependCachePaths(paths)
			} else if err != nil {
				log.Printf("Warning: failed to discover NVMe ZFS pools: %v", err)
			}

			// Wire ZFS mountpoint discovery into the browse handler.
			srv.BrowseHandler().SetZFSLister(&zfsBrowseAdapter{handler: zfsH})
		} else {
			log.Printf("Warning: ZFS support disabled: %v", zfsErr)
		}

		// Register the snapshot manager with the runner so it can save
		// snapshots after each backup/restore operation.
		if hybridMode && snapshotMgr != nil {
			srv.Runner().SetSnapshotManager(snapshotMgr)
			srv.SettingsHandler().SetSnapshotManager(snapshotMgr)

			// Flush the database to USB flash after any config mutation
			// (job/storage/settings/replication CRUD) so the flash copy
			// always has fresh data and survives reboots.
			srv.SetConfigChangeHook(func() {
				if err := snapshotMgr.FlushToUSB(); err != nil {
					log.Printf("Warning: config change USB flush failed: %v", err)
				}
			})

			// Pre-drain shutdown flush: ensure the working DB is on
			// flash BEFORE we wait up to 30 s for runner.Drain to
			// complete. Without this, an in-progress backup that
			// straddles a SIGTERM can leave fresh configuration only
			// in RAM, and rc.vault's SIGKILL escalation 15 s later
			// (extended to 45 s in this same fix) would kill the
			// process before snapshotMgr.Close() ran (issue #108).
			srv.SetPreShutdownHook(func() {
				if err := snapshotMgr.FlushToUSB(); err != nil {
					log.Printf("Warning: pre-shutdown USB flush failed: %v", err)
				}
			})
		}

		// Validate configured paths on startup and log warnings.
		validateConfiguredPaths(database)

		// Register the diagnostics collector.
		diagCollector := diagnostics.NewCollector(database, func() diagnostics.RunnerStatus {
			s := srv.Runner().Status()
			return diagnostics.RunnerStatus{
				Active:          s.Active,
				JobID:           s.JobID,
				JobName:         s.JobName,
				RunType:         s.RunType,
				ItemsTotal:      s.ItemsTotal,
				ItemsDone:       s.ItemsDone,
				ItemsFailed:     s.ItemsFailed,
				CurrentItem:     s.CurrentItem,
				CurrentItemType: s.CurrentItemType,
			}
		}, version, logRing)
		// Wire per-destination dedup stats into diagnostics. The
		// runner owns the dedup repos and the serverKey needed to
		// unseal them; passing a closure avoids an import cycle on
		// internal/runner from internal/diagnostics.
		diagCollector.SetDedupStatsFunc(func(dest db.StorageDestination) (chunks, packs, logical, physical, wasted int64, lastGCAt time.Time, lastGCFreed int64, err error) {
			stats, e := srv.Runner().GetDedupStats(dest)
			if e != nil {
				return 0, 0, 0, 0, 0, time.Time{}, 0, e
			}
			return stats.TotalChunks, stats.TotalPacks, stats.LogicalBytes, stats.PhysicalBytes,
				stats.WastedBytesEstimate, stats.LastGCAt, stats.LastGCFreedBytes, nil
		})
		srv.SettingsHandler().SetDiagnosticsCollector(diagCollector)

		// Start the scheduler with the backup runner.
		sched := scheduler.New(database, func(jobID int64) {
			srv.Runner().RunJob(jobID)
		})

		// Create the replication syncer and register it with the scheduler.
		syncer := replication.NewSyncer(database, srv.Hub())
		syncer.SetServerKey(cfg.ServerKey)
		srv.SetReplicationSyncer(syncer)
		sched.SetReplicationRunner(func(sourceID int64) {
			go func() {
				if _, err := syncer.SyncSource(sourceID, nil); err != nil {
					log.Printf("Replication sync failed for source %d: %v", sourceID, err)
				}
			}()
		})

		// Daily storage destination health check (Feature F).
		sched.SetHealthChecker(func() {
			srv.Runner().RunHealthChecks()
		})

		// Per-job scheduled verification (Feature A).
		sched.SetVerifyRunner(func(jobID int64, mode string) {
			srv.Runner().RunScheduledVerify(jobID, mode)
		})

		// Retry watcher dispatcher (Task 8): polls job_runs.retry_next_at
		// every minute, atomically claims expired rows, and fires retries.
		sched.SetRetryDispatcher(func(jobID, originalRunID int64, attempt int) {
			srv.Runner().RunJobRetry(jobID, originalRunID, attempt)
		})

		if err := sched.Start(); err != nil {
			log.Printf("Warning: scheduler failed to start: %v", err)
		}
		defer sched.Stop()

		srv.SetScheduleReloader(sched.Reload)
		srv.SetNextRunResolver(sched.NextRun)
		// Diagnostics next-run reporter shares the scheduler resolver
		// so per-job rows in scheduler.json show the same timestamp the
		// /api/v1/jobs/next-runs endpoint returns.
		diagCollector.SetNextRunFunc(sched.NextRun)

		// Anomaly detection evaluator. Gated by the anomaly_detection_enabled
		// setting (default "true") so operators can disable it via Settings.
		// buildAnomalyEvaluator is a testable helper that constructs the
		// evaluator and registers the four built-in detectors.
		anomalyEvaluator := buildAnomalyEvaluator(database, srv)
		if anomalyEvaluator != nil {
			anomalyEvaluator.Start()
			srv.Runner().SetEvaluator(anomalyEvaluator)
			srv.SetAnomalyEvaluator(anomalyEvaluator)
		}

		// Heartbeat for external monitoring. Writes to a RAM-backed dir in
		// hybrid mode so it doesn't wear flash. Cancelled by the signal ctx
		// below so it stops cleanly on SIGTERM.
		var hbDir string
		if hybridMode {
			hbDir = "/var/local/vault"
		} else {
			hbDir = filepath.Dir(dbPath)
		}
		hb := runner.NewHeartbeat(filepath.Join(hbDir, "heartbeat"), version, 30*time.Second)

		// Listen for OS signals for graceful shutdown.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		hb.Start(ctx)

		// Periodic USB safety-net refresh. Belt-and-braces for the
		// case where event-driven flushes (configChangeHook on every
		// mutation, post-backup snapshot, SIGTERM pre-drain) all
		// happen to be quiet for a long stretch — e.g. a system that
		// has been idle for hours then loses power unexpectedly. The
		// ticker fires every 30 min; the underlying USB write is
		// gated by the existing 1 h usbMinInterval throttle, so wear
		// is bounded to one write per hour regardless. The
		// pool-snapshot half always runs (zero flash cost). Hybrid
		// mode only; in USB-direct mode the working DB IS the USB
		// file so a "USB backup" would be a no-op self-copy
		// (issue #108).
		if hybridMode && snapshotMgr != nil {
			go func() {
				ticker := time.NewTicker(30 * time.Minute)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if err := snapshotMgr.SaveSnapshotAndUSBBackup(); err != nil {
							log.Printf("Warning: periodic snapshot save failed: %v", err)
						}
					}
				}
			}()
		}

		// Anomaly trend ticker: runs EvaluateTrendDetectors every 5 minutes
		// and prunes old terminal anomalies on each tick. Cancelled by ctx
		// (the same signal context used for all other goroutines).
		if anomalyEvaluator != nil {
			go runAnomalyTrendTicker(ctx, anomalyEvaluator)
		}

		err = srv.StartWithContext(ctx)
		if errors.Is(err, http.ErrServerClosed) {
			// Drain the anomaly evaluator so any in-flight per-run evaluations
			// complete before we exit. Uses a 5-second timeout; on timeout we
			// log a warning and continue with shutdown — the runner has already
			// drained at this point so no new work can arrive.
			if anomalyEvaluator != nil {
				drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if drainErr := anomalyEvaluator.Drain(drainCtx); drainErr != nil {
					log.Printf("WARN anomaly: drain incomplete at shutdown: %v", drainErr)
				}
				drainCancel()
			}

			// Flush the working DB to the persistent snapshot before exiting.
			if hybridMode && snapshotMgr != nil {
				if snapErr := snapshotMgr.Close(); snapErr != nil {
					log.Printf("Warning: final snapshot save failed: %v", snapErr)
				}
			}
			log.Println("Vault daemon stopped gracefully")
			return nil
		}
		return err
	},
}

func init() {
	daemonCmd.Flags().String("db", "/boot/config/plugins/vault/vault.db", "Database path")
	daemonCmd.Flags().String("addr", ":24085", "API listen address")
	daemonCmd.Flags().String("tls-cert", "", "Path to TLS certificate file")
	daemonCmd.Flags().String("tls-key", "", "Path to TLS private key file")
	rootCmd.AddCommand(daemonCmd)
}

// detectBootKind classifies the /boot mount on Unraid as "flash"
// (traditional USB FAT32) or "internal" (Unraid 7.3+ internal-boot
// pool, backed by ZFS). Returns "unknown" when the mount can't be
// classified (non-Linux dev hosts, unusual mounts, etc.). Used only
// to enrich the /api/v1/health response and the startup log; the
// vault.db.backup path is /boot/config/plugins/vault/vault.db.backup
// on both kinds, so persistence behaviour is identical either way.
func detectBootKind() string {
	mi, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "unknown"
	}
	// Each line: <id> <parent> <maj:min> <root> <mount-point> ... - <fstype> <source> ...
	for _, line := range splitLines(mi) {
		// Tokenise; mount point is field 5 (1-indexed), separator " - "
		// then fstype follows. Avoid pulling in regexp for a hot path.
		// Coarse but reliable: look for " /boot " followed by " - <fs>".
		idx := indexOfMountPoint(line, "/boot")
		if idx < 0 {
			continue
		}
		fstype := fstypeAfterSep(line)
		if fstype == "" {
			return "unknown"
		}
		switch fstype {
		case "vfat", "msdos":
			return "flash"
		case "zfs", "btrfs", "ext4", "xfs":
			return "internal"
		default:
			return "unknown"
		}
	}
	return "unknown"
}

// splitLines splits a byte slice on '\n' without allocating per-line
// substrings beyond the slice header. Empty trailing line is skipped.
func splitLines(b []byte) []string {
	out := make([]string, 0, 32)
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, string(b[start:i]))
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, string(b[start:]))
	}
	return out
}

// indexOfMountPoint returns a non-negative offset if the line's
// mount-point field (5th whitespace-separated token) equals target.
// Returns -1 otherwise.
func indexOfMountPoint(line, target string) int {
	fields := splitFields(line)
	if len(fields) < 5 {
		return -1
	}
	if fields[4] == target {
		return 4
	}
	return -1
}

// fstypeAfterSep returns the token immediately after the " - "
// separator that mountinfo uses to delimit per-mount and per-fs
// data. Empty string if absent.
func fstypeAfterSep(line string) string {
	fields := splitFields(line)
	for i, f := range fields {
		if f == "-" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// splitFields splits on any whitespace, like strings.Fields but
// allocation-light for the mountinfo hot path. Returns at most 64
// fields, which is well above mountinfo's worst case.
func splitFields(s string) []string {
	out := make([]string, 0, 16)
	start := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		isSpace := c == ' ' || c == '\t'
		if !isSpace && start == -1 {
			start = i
		} else if isSpace && start != -1 {
			out = append(out, s[start:i])
			start = -1
			if len(out) >= 64 {
				return out
			}
		}
	}
	if start != -1 {
		out = append(out, s[start:])
	}
	return out
}

// copyFile copies src to dst with O_TRUNC+0o600 perms and fsyncs before
// returning. Used by the non-hybrid USB-backup restoration fallback so the
// restored vault.db lands durably on flash before db.Open sees it.
func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 — vault-controlled USB backup path
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()                                       // #nosec G104 — best-effort
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 — vault-controlled DB path
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close() // #nosec G104 — error path
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close() // #nosec G104 — error path
		return err
	}
	return out.Close()
}

// buildAnomalyEvaluator constructs and wires the anomaly Evaluator when the
// anomaly_detection_enabled setting is "true" (the default). Returns nil when
// detection is disabled — the caller must treat nil as a no-op.
//
// The helper is extracted from RunE so the gating logic is unit-testable
// without spinning up the full daemon (see daemon_test.go).
func buildAnomalyEvaluator(database *db.DB, srv *api.Server) *anomaly.Evaluator {
	enabled, _ := database.GetSetting("anomaly_detection_enabled", "true")
	if enabled != "true" {
		return nil
	}

	reg := &anomaly.Registry{}
	reg.Register(anomaly.NewSizeDriftDetector())
	reg.Register(anomaly.NewDurationDriftDetector())
	reg.Register(anomaly.NewReliabilityDetector(database))
	reg.Register(anomaly.NewCapacityTrajectoryDetector(database))

	ev := anomaly.NewEvaluator(database, srv.Hub(), reg, anomaly.RealClock{})

	// Wire the notifier so anomaly raise/escalation events are dispatched to
	// Unraid + Discord. The webhook URL closure reads the DB at send time so
	// settings changes take effect without restarting the daemon.
	notifier := anomaly.NewRealNotifier(func() string {
		url, _ := database.GetSetting("discord_webhook_url", "")
		return url
	})
	ev.SetNotifier(notifier)

	return ev
}

// runAnomalyTrendTicker fires EvaluateTrendDetectors and pruneOldAnomalies every
// 5 minutes until ctx is cancelled. The ticker is stopped cleanly on cancellation.
//
// Pruning is piggybacked onto the trend tick (no separate maintenance ticker
// exists in daemon.go); both operations are fast enough to run on every tick.
func runAnomalyTrendTicker(ctx context.Context, ev *anomaly.Evaluator) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ev.EvaluateTrendDetectors()
			ev.PruneOldAnomalies()
		}
	}
}

// zfsBrowseAdapter bridges engine.ZFSHandler to the browse handler's
// ZFSMountpointLister interface.
type zfsBrowseAdapter struct {
	handler *engine.ZFSHandler
}

func (a *zfsBrowseAdapter) ListZFSMountpoints() ([]handlers.ZFSMountInfo, error) {
	pools, err := a.handler.ListZFSMountpoints()
	if err != nil {
		return nil, fmt.Errorf("listing ZFS mountpoints for browse: %w", err)
	}
	result := make([]handlers.ZFSMountInfo, len(pools))
	for i, p := range pools {
		result[i] = handlers.ZFSMountInfo{Name: p.Name, Mountpoint: p.Mountpoint}
	}
	return result, nil
}

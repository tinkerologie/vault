package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ruaan-deysel/vault/internal/api"
	"github.com/ruaan-deysel/vault/internal/config"
	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/diagnostics"
	"github.com/ruaan-deysel/vault/internal/replication"
	"github.com/ruaan-deysel/vault/internal/scheduler"
	"github.com/ruaan-deysel/vault/internal/tempdir"
	"github.com/ruaan-deysel/vault/internal/unraid"
	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the Vault daemon (API server + scheduler)",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")
		addr, _ := cmd.Flags().GetString("addr")
		tlsCert, _ := cmd.Flags().GetString("tls-cert")
		tlsKey, _ := cmd.Flags().GetString("tls-key")

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

		detectedPool = unraid.PreferredPool()
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
		}

		database, err := db.Open(actualDBPath)
		if err != nil {
			return err
		}
		defer func() { database.Close() }()

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
		}

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

		log.Println("Starting Vault daemon...")

		cfg := api.ServerConfig{
			Addr:      addr,
			TLSCert:   tlsCert,
			TLSKey:    tlsKey,
			ServerKey: serverKey,
			Version:   version,
		}
		srv := api.NewServer(database, cfg)

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
		}, version)
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

		if err := sched.Start(); err != nil {
			log.Printf("Warning: scheduler failed to start: %v", err)
		}
		defer sched.Stop()

		srv.SetScheduleReloader(sched.Reload)
		srv.SetNextRunResolver(sched.NextRun)

		// Listen for OS signals for graceful shutdown.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		err = srv.StartWithContext(ctx)
		if errors.Is(err, http.ErrServerClosed) {
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

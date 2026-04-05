package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ruaan-deysel/vault/internal/api"
	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/replication"
	"github.com/ruaan-deysel/vault/internal/scheduler"
	"github.com/ruaan-deysel/vault/internal/tempdir"
	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the Vault daemon (API server + scheduler)",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")
		addr, _ := cmd.Flags().GetString("addr")
		apiKey, _ := cmd.Flags().GetString("api-key")
		tlsCert, _ := cmd.Flags().GetString("tls-cert")
		tlsKey, _ := cmd.Flags().GetString("tls-key")

		// Environment variables override flags.
		if envKey := os.Getenv("VAULT_API_KEY"); envKey != "" && apiKey == "" {
			apiKey = envKey
		}

		// Hybrid mode detection: if a cache drive is available, use a RAM-backed
		// working database with periodic snapshots to the cache drive. This
		// avoids USB flash wear from SQLite WAL writes.
		var (
			hybridMode   bool
			snapshotPath string
			snapshotMgr  *db.SnapshotManager
			actualDBPath = dbPath
		)

		if info, err := os.Stat("/mnt/cache"); err == nil && info.IsDir() {
			hybridMode = true
			workingDir := "/var/local/vault"
			if err := os.MkdirAll(workingDir, 0o750); err != nil {
				return fmt.Errorf("creating hybrid working directory: %w", err)
			}
			actualDBPath = filepath.Join(workingDir, "vault.db")
			snapshotPath = "/mnt/cache/.vault/vault.db"

			// First-run migration: if the USB DB exists and no snapshot exists
			// yet, copy the USB DB to the snapshot path so it becomes the seed.
			if _, usbErr := os.Stat(dbPath); usbErr == nil {
				if _, snapErr := os.Stat(snapshotPath); os.IsNotExist(snapErr) {
					log.Printf("Migrating USB database %s → snapshot %s", dbPath, snapshotPath)
					if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o750); err != nil {
						return fmt.Errorf("creating snapshot directory: %w", err)
					}
					// Use SQLite backup API via a temporary open to migrate safely.
					tmpDB, err := db.Open(dbPath)
					if err != nil {
						return fmt.Errorf("opening USB database for migration: %w", err)
					}
					migrator := db.NewSnapshotManager(tmpDB, snapshotPath)
					if err := migrator.SaveSnapshot(); err != nil {
						_ = tmpDB.Close()
						return fmt.Errorf("migrating USB database to snapshot: %w", err)
					}
					_ = tmpDB.Close()

					// Remove USB DB to prevent re-migration on next startup.
					// The data is safely on the cache drive now.
					if err := os.Remove(dbPath); err != nil {
						log.Printf("Warning: failed to remove migrated USB database: %v", err)
					} else {
						log.Printf("USB database migrated and removed: %s", dbPath)
					}
					// Clean up any leftover .migrated file from older versions.
					_ = os.Remove(dbPath + ".migrated")
				}
			}

			log.Printf("Hybrid mode: working DB at %s, snapshots at %s", actualDBPath, snapshotPath)
		} else {
			log.Println("Warning: no cache drive detected at /mnt/cache — database writes go directly to USB flash (increased wear)")
		}

		database, err := db.Open(actualDBPath)
		if err != nil {
			return err
		}
		defer func() { database.Close() }()

		// In hybrid mode, restore the latest snapshot into the working DB,
		// then close and re-open so schema migrations run on the restored data.
		if hybridMode {
			snapshotMgr = db.NewSnapshotManager(database, snapshotPath)
			if err := snapshotMgr.RestoreFromSnapshot(); err != nil {
				return fmt.Errorf("restoring snapshot: %w", err)
			}
			_ = database.Close()
			database, err = db.Open(actualDBPath)
			if err != nil {
				return fmt.Errorf("re-opening database after snapshot restore: %w", err)
			}
			// Check for a user-configured snapshot path override.
			if override, err := database.GetSetting("snapshot_path_override", ""); err == nil && override != "" {
				snapshotPath = override
				log.Printf("Using custom snapshot path: %s", snapshotPath)
				if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o750); err != nil {
					log.Printf("Warning: failed to create custom snapshot directory: %v", err)
				}
			}
			// Re-create the snapshot manager with the fresh DB handle.
			snapshotMgr = db.NewSnapshotManager(database, snapshotPath)
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
		if apiKey != "" {
			log.Println("API key authentication enabled")
		}

		// If an API key was provided via CLI/env var and the DB has no key yet,
		// seed it into the database so it can be managed from the Settings UI.
		if apiKey != "" && !database.HasAPIKey() {
			sealed, sealErr := crypto.Seal(serverKey, apiKey)
			if sealErr != nil {
				return fmt.Errorf("sealing api key: %w", sealErr)
			}
			hash, hashErr := crypto.HashPassphrase(apiKey)
			if hashErr != nil {
				return fmt.Errorf("hashing api key: %w", hashErr)
			}
			if err := database.SetSetting("api_key_sealed", sealed); err != nil {
				return fmt.Errorf("storing api key: %w", err)
			}
			if err := database.SetSetting("api_key_hash", hash); err != nil {
				return fmt.Errorf("storing api key hash: %w", err)
			}
			log.Println("CLI API key seeded into database")
		}

		if err := validateListenAddress(addr, apiKey != "" || database.HasAPIKey()); err != nil {
			return err
		}

		cfg := api.ServerConfig{
			Addr:      addr,
			APIKey:    apiKey,
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
		}

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

func validateListenAddress(addr string, hasAPIKey bool) error {
	if isLoopbackListenAddress(addr) {
		return nil
	}
	if hasAPIKey {
		return nil
	}

	return fmt.Errorf(
		"non-loopback bind address %q requires an API key; generate one first while Vault is bound to 127.0.0.1",
		addr,
	)
}

func isLoopbackListenAddress(addr string) bool {
	host := listenAddressHost(addr)
	if host == "" {
		return false
	}

	normalized := strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(normalized, "localhost") {
		return true
	}

	ip := net.ParseIP(normalized)
	return ip != nil && ip.IsLoopback()
}

func listenAddressHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	return addr
}

func init() {
	daemonCmd.Flags().String("db", "/boot/config/plugins/vault/vault.db", "Database path")
	daemonCmd.Flags().String("addr", ":24085", "API listen address")
	daemonCmd.Flags().String("api-key", "", "API key for authentication (or set VAULT_API_KEY)")
	daemonCmd.Flags().String("tls-cert", "", "Path to TLS certificate file")
	daemonCmd.Flags().String("tls-key", "", "Path to TLS private key file")
	rootCmd.AddCommand(daemonCmd)
}

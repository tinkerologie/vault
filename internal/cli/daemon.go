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

	"github.com/ruaandeysel/vault/internal/api"
	"github.com/ruaandeysel/vault/internal/crypto"
	"github.com/ruaandeysel/vault/internal/db"
	"github.com/ruaandeysel/vault/internal/replication"
	"github.com/ruaandeysel/vault/internal/scheduler"
	"github.com/ruaandeysel/vault/internal/tempdir"
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

		database, err := db.Open(dbPath)
		if err != nil {
			return err
		}
		defer database.Close()

		// Prune activity log entries older than 90 days on startup.
		if err := database.DeleteOldActivityLogs(90); err != nil {
			log.Printf("Warning: failed to prune old activity logs: %v", err)
		}

		// Mark any "running" job runs as failed — they were interrupted by
		// a previous daemon crash or restart.
		if cleaned, err := database.CleanupStaleRuns(); err != nil {
			log.Printf("Warning: failed to clean up stale job runs: %v", err)
		} else if cleaned > 0 {
			log.Printf("Cleaned up %d stale job run(s) from previous session", cleaned)
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

		cfg := api.ServerConfig{
			Addr:      addr,
			APIKey:    apiKey,
			TLSCert:   tlsCert,
			TLSKey:    tlsKey,
			ServerKey: serverKey,
			Version:   version,
		}
		srv := api.NewServer(database, cfg)

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
			log.Println("Vault daemon stopped gracefully")
			return nil
		}
		return err
	},
}

func init() {
	daemonCmd.Flags().String("db", "/boot/config/plugins/vault/vault.db", "Database path")
	daemonCmd.Flags().String("addr", ":24085", "API listen address")
	daemonCmd.Flags().String("api-key", "", "API key for authentication (or set VAULT_API_KEY)")
	daemonCmd.Flags().String("tls-cert", "", "Path to TLS certificate file")
	daemonCmd.Flags().String("tls-key", "", "Path to TLS private key file")
	rootCmd.AddCommand(daemonCmd)
}

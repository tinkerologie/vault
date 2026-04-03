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
	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/replication"
	"github.com/ruaan-deysel/vault/internal/scheduler"
	"github.com/spf13/cobra"
)

var replicaCmd = &cobra.Command{
	Use:   "replica",
	Short: "Start a read-only Vault replica (replication receiver only)",
	Long: `Start a minimal Vault daemon that receives replicated backups from a
remote Vault server. This mode does not create backups — it only pulls
and stores replicated data. All backup write endpoints are disabled.

Designed to run as a Docker container on any Linux host for off-site
disaster recovery.`,
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

		// Ensure the database directory exists.
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
			return fmt.Errorf("creating database directory: %w", err)
		}

		database, err := db.Open(dbPath)
		if err != nil {
			return err
		}
		defer database.Close()

		// Prune old activity logs.
		if err := database.DeleteOldActivityLogs(90); err != nil {
			log.Printf("Warning: failed to prune old activity logs: %v", err)
		}
		if err := database.CapActivityLogs(10000); err != nil {
			log.Printf("Warning: failed to cap activity logs: %v", err)
		}

		// Load or generate the server key for sealing secrets at rest.
		keyPath := filepath.Join(filepath.Dir(dbPath), "vault.key")
		serverKey, err := crypto.LoadOrCreateServerKey(keyPath)
		if err != nil {
			return fmt.Errorf("loading server key: %w", err)
		}

		log.Println("Starting Vault replica daemon (read-only mode)...")
		if apiKey != "" {
			log.Println("API key authentication enabled")
		}

		// Seed CLI API key into the database if needed.
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
			ReadOnly:  true,
		}
		srv := api.NewServer(database, cfg)

		// Create the replication syncer (core functionality of replica mode).
		syncer := replication.NewSyncer(database, srv.Hub())
		syncer.SetServerKey(cfg.ServerKey)
		srv.SetReplicationSyncer(syncer)

		// Start the scheduler for replication sync schedules only.
		// The backup runner callback is a no-op since replicas don't run backups.
		sched := scheduler.New(database, func(_ int64) {})
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

		// Listen for OS signals for graceful shutdown.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		err = srv.StartWithContext(ctx)
		if errors.Is(err, http.ErrServerClosed) {
			log.Println("Vault replica daemon stopped gracefully")
			return nil
		}
		return err
	},
}

func init() {
	replicaCmd.Flags().String("db", "/data/vault.db", "Database path")
	replicaCmd.Flags().String("addr", ":24085", "API listen address")
	replicaCmd.Flags().String("api-key", "", "API key for authentication (or set VAULT_API_KEY)")
	replicaCmd.Flags().String("tls-cert", "", "Path to TLS certificate file")
	replicaCmd.Flags().String("tls-key", "", "Path to TLS private key file")
	rootCmd.AddCommand(replicaCmd)
}

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
		tlsCert, _ := cmd.Flags().GetString("tls-cert")
		tlsKey, _ := cmd.Flags().GetString("tls-key")

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

		cfg := api.ServerConfig{
			Addr:      addr,
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
	replicaCmd.Flags().String("tls-cert", "", "Path to TLS certificate file")
	replicaCmd.Flags().String("tls-key", "", "Path to TLS private key file")
	rootCmd.AddCommand(replicaCmd)
}

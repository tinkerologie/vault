//go:build linux

package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/mount"
)

var (
	mountCmd = &cobra.Command{
		Use:   "mount [flags]",
		Short: "Mount a deduplicated backup restore point as a read-only FUSE filesystem",
		Long: `Mount a deduplicated backup restore point as a read-only FUSE filesystem.
Allows browsing files, comparing versions across backups, and extracting individual files without a full restore.`,
		RunE: runMountCmd,
	}

	mountJobID int64
	mountRPID  int64
	mountDBVal string
	mountKey   string
	mountDir   string
)

func init() {
	mountCmd.Flags().Int64Var(&mountJobID, "job", 0, "backup job ID (required)")
	mountCmd.Flags().Int64Var(&mountRPID, "restore-point", 0, "restore point ID (required)")
	mountCmd.Flags().StringVar(&mountDBVal, "db", defaultDedupDBPath, "path to vault.db")
	mountCmd.Flags().StringVar(&mountKey, "key", "", "path to vault.key (default: <dir of --db>/vault.key)")
	mountCmd.Flags().StringVar(&mountDir, "target", "", "target mount directory (default: /mnt/vault-fuse/mount-<id>)")
	_ = mountCmd.MarkFlagRequired("job")
	_ = mountCmd.MarkFlagRequired("restore-point")

	rootCmd.AddCommand(mountCmd)
}

func runMountCmd(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	database, err := db.Open(mountDBVal)
	if err != nil {
		return fmt.Errorf("open db %s: %w", mountDBVal, err)
	}
	defer database.Close()

	keyPath := mountKey
	if keyPath == "" {
		keyPath = filepath.Join(filepath.Dir(mountDBVal), "vault.key")
	}
	serverKey, err := loadServerKeyAtPath(keyPath)
	if err != nil {
		return fmt.Errorf("read server key from %s: %w", keyPath, err)
	}

	mgr := mount.NewManager(database, nil, serverKey)

	session, err := mgr.MountRestorePointTo(ctx, mountJobID, mountRPID, mountDir)
	if err != nil {
		return fmt.Errorf("mount restore point: %w", err)
	}

	fmt.Printf("FUSE backup mounted at %s (session ID: %d)\n", session.MountPath, session.ID)
	fmt.Println("Press Ctrl+C to unmount and exit...")

	<-ctx.Done()
	fmt.Println("\nUnmounting...")
	if err := mgr.Unmount(context.Background(), session.ID); err != nil {
		return fmt.Errorf("unmount failed: %w", err)
	}
	log.Println("Unmount complete.")
	return nil
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/docsmeta"
	"github.com/tinkerologie/vault/internal/maintlock"
	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/storage"
)

// defaultDedupDBPath mirrors the daemon's --db default so the CLI works
// out-of-the-box on an Unraid host.
const defaultDedupDBPath = "/boot/config/plugins/vault/vault.db"

var (
	dedupCmd = &cobra.Command{
		Use:   "dedup",
		Short: "Deduplication maintenance subcommands",
	}

	dedupRepairCmd = &cobra.Command{
		Use:   "repair",
		Short: "Rebuild the SQLite dedup index for one destination from on-storage JSONL blobs",
		Long: `Rebuild the SQLite dedup_packs and dedup_chunks rows for one
storage destination by walking the on-storage <dest>/_vault/index/*.idx
blobs. Use when the local DB is lost or corrupted but the destination's
packs and index blobs are intact.

The command is read-only against the destination's pack storage; it only
writes to the local SQLite database.

By default vault.key is read from the same directory as --db; use --key to point at a key restored to a different location (e.g. during disaster recovery).`,
		RunE: runDedupRepair,
	}

	dedupGCCmd = &cobra.Command{
		Use:   "gc",
		Short: "Run mark-and-sweep garbage collection for a dedup destination",
		Long: `Mirror of the POST /api/v1/storage/{id}/gc HTTP endpoint, for
scripted maintenance. Walks every live restore point's manifest, marks
reachable chunks, then deletes packs whose every chunk is unreachable.

Mixed packs (some live, some dead chunks) are left in place — reported as
"rewritable bytes" only. Compaction is a separate future operation.

Refuses to run while the Vault service is running; use the HTTP endpoint then. GC also stops, deleting nothing, if any restore point's manifest cannot be read.

By default vault.key is read from the same directory as --db; use --key to point at a key restored to a different location (e.g. during disaster recovery).`,
		RunE: runDedupGC,
	}

	// Subcommand-local flag values. Following the existing pattern in
	// daemon.go / replica.go each subcommand owns its own flag bindings.
	dedupDestID      int64
	dedupRepairDBVal string
	dedupGCDBVal     string
	dedupRepairKey   string
	dedupGCKey       string
)

func init() {
	dedupRepairCmd.Flags().Int64Var(&dedupDestID, "dest", 0, "storage destination ID (required)")
	dedupRepairCmd.Flags().StringVar(&dedupRepairDBVal, "db", defaultDedupDBPath, "path to vault.db")
	dedupRepairCmd.Flags().StringVar(&dedupRepairKey, "key", "", "path to vault.key (default: <dir of --db>/vault.key)")
	_ = dedupRepairCmd.MarkFlagRequired("dest")

	dedupGCCmd.Flags().Int64Var(&dedupDestID, "dest", 0, "storage destination ID (required)")
	dedupGCCmd.Flags().StringVar(&dedupGCDBVal, "db", defaultDedupDBPath, "path to vault.db")
	dedupGCCmd.Flags().StringVar(&dedupGCKey, "key", "", "path to vault.key (default: <dir of --db>/vault.key)")
	_ = dedupGCCmd.MarkFlagRequired("dest")

	dedupCmd.AddCommand(dedupRepairCmd, dedupGCCmd)
	rootCmd.AddCommand(dedupCmd)
}

// dedupContext bundles the resources a dedup CLI subcommand needs.
type dedupContext struct {
	repo    *dedup.Repo
	db      *db.DB
	adapter storage.Adapter
	destID  int64
}

// openDedupContext is the shared setup for both subcommands: it opens the
// SQLite database, loads the destination row, asserts dedup is enabled,
// reads the server key from disk, builds the storage adapter, and opens
// the dedup repo. The returned cleanup closes the database (the storage
// Adapter interface has no Close — adapters that hold resources clean up
// on GC / inside individual operations).
func openDedupContext(dbPath, keyPath string) (*dedupContext, func(), error) {
	if dedupDestID == 0 {
		return nil, nil, fmt.Errorf("--dest is required")
	}

	database, err := db.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open db %s: %w", dbPath, err)
	}

	dest, err := database.GetStorageDestination(dedupDestID)
	if err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("get destination %d: %w", dedupDestID, err)
	}
	if !dest.DedupEnabled {
		database.Close()
		return nil, nil, fmt.Errorf("destination %d (%q) is not dedup-enabled", dedupDestID, dest.Name)
	}

	if keyPath == "" {
		keyPath = filepath.Join(filepath.Dir(dbPath), "vault.key")
	}
	serverKey, err := loadServerKeyAtPath(keyPath)
	if err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("load server key at %s: %w", keyPath, err)
	}

	// The row was read before the key was known; its secrets need the key.
	database.SetCredentialKey(serverKey)
	dest, err = database.GetStorageDestination(dedupDestID)
	if err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("get destination %d: %w", dedupDestID, err)
	}

	storage.SetHostKeyPinner(database.PinSFTPHostKey)
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("build storage adapter: %w", err)
	}

	repo, err := dedup.OpenRepo(database, adapter, dest.ID, serverKey)
	if err != nil {
		database.Close()
		if errors.Is(err, dedup.ErrServerKeyMismatch) {
			return nil, nil, fmt.Errorf("open dedup repo: %s is not the vault.key this destination was created with; point --key at the original vault.key, or restore the database from the Recovery wizard with your backup password: %w", keyPath, err)
		}
		return nil, nil, fmt.Errorf("open dedup repo: %w", err)
	}

	ctx := &dedupContext{
		repo:    repo,
		db:      database,
		adapter: adapter,
		destID:  dest.ID,
	}
	cleanup := func() {
		database.Close()
	}
	return ctx, cleanup, nil
}

// loadServerKeyAtPath reads the server key file from disk and validates
// its size. Unlike crypto.LoadOrCreateServerKey this never creates a new
// key — a missing key file is a hard error for CLI flows because writing
// a fresh random key here would silently break every subsequent unseal.
func loadServerKeyAtPath(p string) ([]byte, error) {
	b, err := os.ReadFile(p) // #nosec G304 // path is from CLI flags (--key or derived from --db) — admin-supplied, not user input
	if err != nil {
		return nil, err
	}
	if len(b) != crypto.ServerKeySize {
		return nil, fmt.Errorf("server key at %s has unexpected size %d (want %d)",
			p, len(b), crypto.ServerKeySize)
	}
	return b, nil
}

func runDedupRepair(_ *cobra.Command, _ []string) error {
	lock, err := lockForDedupMaintenance()
	if err != nil {
		return err
	}
	defer lock.Release()

	ctx, cleanup, err := openDedupContext(dedupRepairDBVal, dedupRepairKey)
	if err != nil {
		return err
	}
	defer cleanup()

	idx := dedup.NewIndex(ctx.db, ctx.adapter, ctx.destID)
	if err := idx.RebuildFromStorage(); err != nil {
		return fmt.Errorf("rebuild dedup index: %w", err)
	}

	stats := ctx.repo.Stats()
	fmt.Printf("dedup repair: rebuilt %d packs / %d chunks for destination %d\n",
		stats.TotalPacks, stats.TotalChunks, ctx.destID)
	return nil
}

func runDedupGC(_ *cobra.Command, _ []string) error {
	lock, err := lockForDedupMaintenance()
	if err != nil {
		return err
	}
	defer lock.Release()

	ctx, cleanup, err := openDedupContext(dedupGCDBVal, dedupGCKey)
	if err != nil {
		return err
	}
	defer cleanup()

	live, err := collectLiveManifestIDsForCLI(ctx.repo, ctx.db, ctx.destID)
	if err != nil {
		return fmt.Errorf("collect live manifest IDs: %w", err)
	}

	ratioStr, _ := ctx.db.GetSetting("dedup_compaction_min_dead_ratio", docsmeta.DefaultFor("dedup_compaction_min_dead_ratio"))
	ratio, perr := strconv.ParseFloat(ratioStr, 64)
	if perr != nil || ratio < 0 || ratio > 1 {
		fmt.Fprintf(os.Stderr, "warning: invalid dedup_compaction_min_dead_ratio %q, using 0.5 (err: %v)\n", ratioStr, perr)
		ratio = 0.5
	}
	result, err := dedup.RunGC(ctx.repo, live, dedup.GCOptions{CompactMinDeadRatio: ratio})
	if err != nil {
		return fmt.Errorf("run gc: %w", err)
	}

	fmt.Printf("dedup gc: freed %d packs (%d bytes); compacted %d packs (reclaimed %d bytes); rewritable=%d bytes; errors=%d\n",
		result.FreedPacks, result.FreedBytes,
		result.CompactedPacks, result.ReclaimedBytes,
		result.RewritableBytes, len(result.Errors))
	for _, e := range result.Errors {
		fmt.Fprintf(os.Stderr, "  error: %s\n", e)
	}
	return nil
}

// collectLiveManifestIDsForCLI is the daemon's reachability walk, so the two
// can never disagree about what GC may delete.
func collectLiveManifestIDsForCLI(repo *dedup.Repo, d *db.DB, destID int64) ([]dedup.ID, error) {
	return runner.CollectLiveManifestIDs(repo, d, destID)
}

// lockForDedupMaintenance takes the host-wide maintenance lock, refusing while
// a daemon runs: its backups register packs before their restore points exist,
// so a sweep from here would delete them, and in hybrid mode it works from a
// different database file than --db.
func lockForDedupMaintenance() (*maintlock.HostLock, error) {
	lock, err := maintlock.AcquireExclusive()
	if errors.Is(err, maintlock.ErrBusy) {
		return nil, fmt.Errorf("the Vault service or another vault dedup command is running; use Run cleanup on the Storage page (POST /api/v1/storage/{id}/gc), or stop the service first: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot confirm nothing else is writing to the repository: %w", err)
	}
	return lock, nil
}

// holdMaintenanceLock takes the host lock's shared side for a daemon's lifetime,
// so vault dedup commands refuse to run alongside it. A daemon must still start
// if the lock is unusable; the CLI side treats that same failure as a refusal.
func holdMaintenanceLock(ctx context.Context) (release func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	lock, err := maintlock.AcquireShared(ctx)
	if err != nil {
		log.Printf("Warning: maintenance lock unavailable: %v", err)
		return func() {}
	}
	return lock.Release
}

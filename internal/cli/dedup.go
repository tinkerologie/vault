package cli

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/storage"
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

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("build storage adapter: %w", err)
	}

	repo, err := dedup.OpenRepo(database, adapter, dest.ID, serverKey)
	if err != nil {
		database.Close()
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
	ctx, cleanup, err := openDedupContext(dedupGCDBVal, dedupGCKey)
	if err != nil {
		return err
	}
	defer cleanup()

	live, err := collectLiveManifestIDsForCLI(ctx.repo, ctx.db, ctx.destID)
	if err != nil {
		return fmt.Errorf("collect live manifest IDs: %w", err)
	}

	result, err := dedup.RunGC(ctx.repo, live, dedup.GCOptions{})
	if err != nil {
		return fmt.Errorf("run gc: %w", err)
	}

	fmt.Printf("dedup gc: freed %d packs (%d bytes); rewritable=%d bytes; errors=%d\n",
		result.FreedPacks, result.FreedBytes, result.RewritableBytes, len(result.Errors))
	for _, e := range result.Errors {
		fmt.Fprintf(os.Stderr, "  error: %s\n", e)
	}
	return nil
}

// collectLiveManifestIDsForCLI mirrors runner.collectLiveManifestIDs. We
// can't call the runner method directly without spinning up a full Runner
// instance (which would pull in the entire engine + scheduler), so this
// helper duplicates the (small) query. Keep in lockstep with runner.go —
// including the engine.WalkManifestClosure expansion that pulls in
// container-volume sub-manifests so GC never sweeps nested data chunks.
func collectLiveManifestIDsForCLI(repo *dedup.Repo, d *db.DB, destID int64) ([]dedup.ID, error) {
	rows, err := d.Query(`
        SELECT manifest_id, metadata
          FROM restore_points
         WHERE job_id IN (SELECT id FROM jobs WHERE storage_dest_id = ?)`, destID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tops := make([]dedup.ID, 0, 16)
	seenTop := make(map[dedup.ID]struct{})
	addTop := func(id dedup.ID) {
		if _, ok := seenTop[id]; ok {
			return
		}
		seenTop[id] = struct{}{}
		tops = append(tops, id)
	}
	for rows.Next() {
		var (
			mID      []byte
			metadata sql.NullString
		)
		if err := rows.Scan(&mID, &metadata); err != nil {
			return nil, err
		}
		if len(mID) == 32 {
			var id dedup.ID
			copy(id[:], mID)
			addTop(id)
		}
		if metadata.Valid && metadata.String != "" {
			var meta map[string]any
			if err := json.Unmarshal([]byte(metadata.String), &meta); err == nil {
				if im, ok := meta["item_manifests"].(map[string]any); ok {
					for _, v := range im {
						hexStr, ok := v.(string)
						if !ok || hexStr == "" {
							continue
						}
						raw, derr := hex.DecodeString(hexStr)
						if derr != nil || len(raw) != 32 {
							continue
						}
						var id dedup.ID
						copy(id[:], raw)
						addTop(id)
					}
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Expand each top-level manifest through its container-volume
	// sub-manifests so GC marks the nested data chunks too.
	seen := make(map[dedup.ID]struct{})
	out := make([]dedup.ID, 0, len(tops))
	for _, top := range tops {
		manifests, _, werr := engine.WalkManifestClosure(repo, []dedup.ID{top})
		if werr != nil {
			fmt.Fprintf(os.Stderr, "  warning: skipping unreadable manifest %x: %v\n", top[:8], werr)
			if _, ok := seen[top]; !ok {
				seen[top] = struct{}{}
				out = append(out, top)
			}
			continue
		}
		for _, id := range manifests {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out, nil
}

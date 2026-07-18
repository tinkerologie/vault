package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ListingSuffix is appended to an archive path to derive the effective-listing
// sidecar filename: data.tar.zst -> data.tar.zst.listing.json.
//
// Unlike the tar index (which describes only the files INSIDE this archive —
// for an incremental, just the changed files), the listing records the FULL
// effective file set of the source at backup time, after exclusions. The
// chain-restore prune pass uses it as the authoritative point-in-time file
// set so files deleted or newly excluded after the base full backup are not
// resurrected by the chain overlay (issue #231).
const ListingSuffix = ".listing.json"

// WriteEffectiveListing walks srcPath with the same exclusion semantics as
// the archive walk and writes the full effective listing sidecar next to the
// archive. Best-effort from the engine's perspective — callers may log and
// ignore failures; without a listing, chain restore simply skips the prune
// pass (its prior behaviour).
func WriteEffectiveListing(srcPath, archivePath string, exclusions []string) error {
	idx := TarIndex{Version: tarIndexVersion, Archive: filepath.Base(archivePath)}

	err := filepath.Walk(srcPath, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			if p == srcPath {
				return fmt.Errorf("listing: source path inaccessible: %w", walkErr)
			}
			return nil // skip inaccessible entries, matching the tar walk
		}
		rel, err := filepath.Rel(srcPath, p)
		if err != nil || rel == "." {
			return nil
		}
		if shouldExcludePath(rel, exclusions) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Match the archive walk's eligibility: special file types are never
		// archived, so they must not appear in the authoritative listing
		// either (a listed-but-unarchivable path would shield a stale file
		// from the prune pass).
		if info.Mode()&(os.ModeSocket|os.ModeCharDevice|os.ModeDevice|os.ModeNamedPipe) != 0 {
			return nil
		}
		idx.Files = append(idx.Files, TarIndexEntry{
			Path:    filepath.ToSlash(rel),
			Size:    info.Size(),
			Mode:    fmt.Sprintf("%04o", info.Mode().Perm()),
			ModTime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z07:00"),
			IsDir:   info.IsDir(),
		})
		return nil
	})
	if err != nil {
		return err
	}

	data, err := json.Marshal(idx)
	if err != nil {
		return fmt.Errorf("listing: marshal: %w", err)
	}
	if err := os.WriteFile(archivePath+ListingSuffix, data, 0600); err != nil {
		return fmt.Errorf("listing: write: %w", err)
	}
	return nil
}

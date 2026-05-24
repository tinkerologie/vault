package engine

import (
	"fmt"
	"strings"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// WalkManifestClosure walks the dedup manifest graph rooted at the given
// top-level manifest IDs and returns every reachable chunk ID, partitioned
// into manifest chunks (the tops plus any nested container-volume
// sub-manifests) and leaf data chunks. The returned slices are de-duplicated.
//
// Why this exists: container manifests reference file data only *indirectly*.
// Each `__vol__<dest>` entry's single chunk is a sub-manifest ID (a
// FolderHandler.BackupChunked manifest) whose own entries list the real data
// chunks. A non-recursive walk over a container manifest therefore never sees
// the volume data, which silently breaks two things:
//
//   - GC reachability — RunGC marks the direct chunks of every "live" manifest.
//     If the live set holds only the top manifest, the volume data chunks are
//     unmarked and any pack that contains only such chunks is swept (data
//     loss). Expanding the live set with the sub-manifest IDs returned here
//     fixes it without changing the GC mark phase.
//   - Deep verify — must re-read and re-hash every chunk a restore would touch,
//     including the volume data one level down.
//
// Folder and plugin top-level manifests have no sub-manifests, so the walk just
// returns their data chunks. Sub-manifest detection is key-aware
// (containerVolPrefix) AND confirmed by a successful manifest decode, so a real
// file literally named like a `__vol__` key is treated as ordinary data rather
// than mis-recursed.
func WalkManifestClosure(repo *dedup.Repo, tops []dedup.ID) (manifests, data []dedup.ID, err error) {
	if repo == nil {
		return nil, nil, fmt.Errorf("WalkManifestClosure: nil repo")
	}
	seen := make(map[dedup.ID]struct{})

	var walk func(id dedup.ID) error
	walk = func(id dedup.ID) error {
		if _, ok := seen[id]; ok {
			return nil
		}
		seen[id] = struct{}{}
		manifests = append(manifests, id)

		m, err := repo.GetManifest(id)
		if err != nil {
			return fmt.Errorf("walk manifest %x: %w", id[:8], err)
		}
		for key, entry := range m.Files {
			isVol := strings.HasPrefix(key, containerVolPrefix)
			for _, c := range entry.Chunks {
				if _, ok := seen[c]; ok {
					continue
				}
				// A __vol__ entry's chunk is expected to be a sub-manifest.
				// Confirm by decoding before recursing so a pathological data
				// chunk under such a key can't derail the walk.
				if isVol {
					if _, derr := repo.GetManifest(c); derr == nil {
						if werr := walk(c); werr != nil {
							return werr
						}
						continue
					}
				}
				seen[c] = struct{}{}
				data = append(data, c)
			}
		}
		return nil
	}

	for _, t := range tops {
		if err := walk(t); err != nil {
			return nil, nil, err
		}
	}
	return manifests, data, nil
}

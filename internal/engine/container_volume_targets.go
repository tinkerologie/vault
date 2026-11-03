package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/moby/moby/client"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/safepath"
)

// ContainerVolumeTarget describes one volume of a staged classic container
// restore point and the on-disk path a restore extracts it into.
type ContainerVolumeTarget struct {
	// Source is the mount's host path at backup time — the durable key that
	// correlates the same volume across chain steps (issue #352).
	Source string
	// Archive is the per-volume archive name recorded in this step's
	// manifest, and therefore the base its sidecars are named after.
	Archive  string
	IsFile   bool
	BackedUp bool
	// Target is where ContainerHandler.Restore extracts this volume.
	Target string
}

// ContainerVolumeTargets resolves where each volume of a staged container
// restore point lands on disk, applying exactly the rules
// ContainerHandler.Restore applies — including the alternate-destination
// rewrite and the named-volume target.
//
// The runner needs this to prune files a chain overlay resurrected (issue
// #345) without duplicating the destination logic; duplicating it would mean
// the prune could operate on paths the restore never wrote.
func ContainerVolumeTargets(sourceDir, restoreDestination string) ([]ContainerVolumeTarget, error) {
	configData, err := os.ReadFile(filepath.Join(sourceDir, "config.json")) // #nosec G304 — sourceDir is a vault-controlled staging directory
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var inspect restoreInspect
	if err := json.Unmarshal(configData, &inspect); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	restoreDest := restoreDestination
	if restoreDest != "" {
		normalized, normErr := normalizeRestorePath(restoreDest)
		if normErr != nil {
			return nil, normErr
		}
		restoreDest = normalized
	}

	manifest, present, err := readVolumeManifest(sourceDir)
	if err != nil {
		return nil, err
	}
	if !present {
		// Without a manifest there is no durable key to correlate volumes by,
		// so the caller must not prune. An empty result says exactly that.
		return nil, nil
	}

	out := make([]ContainerVolumeTarget, 0, len(inspect.Mounts))
	for i, mount := range inspect.Mounts {
		if !backupableMount(mount.Type) || mount.Source == "" || mount.Destination == "" {
			continue
		}
		entry, found := findVolumeManifestEntry(manifest, mount.Source, i)
		if !found {
			continue
		}
		// ContainerHandler.Restore treats both of these as fatal. Here they
		// mean the caller cannot know where this volume landed, so skipping
		// it silently would hide a mismatch behind a prune that simply did
		// not run — say so instead.
		target, targetErr := volumeRestoreTarget(restoreDest, mount.Type, mount.Name, mount.Source)
		if targetErr != nil {
			log.Printf("engine: cannot resolve a restore target for volume %s — it is excluded from the chain prune: %v", mount.Source, targetErr)
			continue
		}
		if restoreDest == "" {
			if _, policyErr := safepath.CheckVolumeTarget(target); policyErr != nil {
				log.Printf("engine: restore target %s is outside the in-place volume locations — it is excluded from the chain prune: %v", target, policyErr)
				continue
			}
		}
		normalized, normErr := normalizeRestorePath(target)
		if normErr != nil {
			log.Printf("engine: restore target %s for volume %s is not an approved path — it is excluded from the chain prune: %v", target, mount.Source, normErr)
			continue
		}
		out = append(out, ContainerVolumeTarget{
			Source:   mount.Source,
			Archive:  entry.Archive,
			IsFile:   entry.IsFile,
			BackedUp: entry.BackedUp,
			Target:   normalized,
		})
	}
	return out, nil
}

// ContainerVolumeArchives maps each backed-up volume's source host path to the
// archive name that step recorded for it.
//
// A chain step names its archives however the Vault that wrote it did — an
// index name before issue #352, a stable-key name after — so a caller walking
// several steps must ask each step what it called a given volume rather than
// assuming one scheme. The returned names are validated, so they are safe to
// join onto the step directory.
func ContainerVolumeArchives(stepDir string) (map[string]string, error) {
	manifest, present, err := readVolumeManifest(stepDir)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	out := make(map[string]string, len(manifest))
	for _, me := range manifest {
		if !me.BackedUp || me.Source == "" || !safeVolumeArchiveName(me.Archive) {
			continue
		}
		out[me.Source] = me.Archive
	}
	return out, nil
}

// authorizeVolumeSources decides whether a backup's mount sources may be written
// in place. Backup metadata is untrusted, so a source must be in an in-place
// volume location or be a bind the container on this host already has. It runs
// before anything is written, and a custom restore destination bypasses it
// because every target is then rewritten beneath that destination.
func authorizeVolumeSources(sources, live []string) error {
	for _, source := range sources {
		// The live exception needs the exact path, with no symlink in it: a link
		// planted since the container was created must not borrow its trust.
		if clean := filepath.Clean(source); slices.Contains(live, clean) {
			if resolved, err := evalExistingPrefix(clean); err == nil && resolved == clean {
				continue
			}
		}
		if _, err := safepath.CheckVolumeTarget(source); err != nil {
			return fmt.Errorf("refusing in-place restore of this container's volume: %w", err)
		}
	}
	return nil
}

// chunkedVolumeSources lists the mount sources a dedup restore would write,
// applying the same skips and selection as restoreChunkedVolumes.
func chunkedVolumeSources(m dedup.Manifest, inspect restoreInspect, selection []string) []string {
	var allDests []string
	for k, v := range m.Files {
		if dest, ok := ContainerVolumeDest(k); ok && !IsSkippedVolumeEntry(v) {
			allDests = append(allDests, dest)
		}
		if dest, ok := ContainerVolumeFileDest(k); ok {
			allDests = append(allDests, dest)
		}
	}
	var sources []string
	for _, mnt := range inspect.Mounts {
		if !backupableMount(mnt.Type) || mnt.Source == "" || mnt.Destination == "" {
			continue
		}
		if _, wanted := containerVolumeIncludes(selection, mnt.Destination, allDests); !wanted {
			continue
		}
		for k, v := range m.Files {
			dest, isFile := ContainerVolumeFileDest(k)
			if !isFile {
				dest = strings.TrimPrefix(k, containerVolPrefix)
				if dest == k {
					continue
				}
			}
			written := v.Size != volumeSkippedSize && (len(v.Chunks) > 0 || (isFile && v.Size == 0))
			if dest == mnt.Destination && written {
				sources = append(sources, mnt.Source)
				break
			}
		}
	}
	return sources
}

// liveMountSources returns the mount sources of the container currently on this
// host under name: a locally trusted record of its binds. None is returned when
// the container is gone or Docker is unavailable.
func (h *ContainerHandler) liveMountSources(ctx context.Context, name string) []string {
	if h == nil || h.cli == nil {
		return nil
	}
	result, err := h.cli.ContainerInspect(ctx, strings.TrimPrefix(name, "/"), client.ContainerInspectOptions{})
	if err != nil {
		return nil
	}
	sources := make([]string, 0, len(result.Container.Mounts))
	for _, m := range result.Container.Mounts {
		if m.Source != "" {
			sources = append(sources, filepath.Clean(m.Source))
		}
	}
	return sources
}

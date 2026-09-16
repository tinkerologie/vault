package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

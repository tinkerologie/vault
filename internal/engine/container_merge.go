package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MergeContainerChainStaging merges per-chain-step staging directories for a
// classic container restore into a single directory that
// ContainerHandler.Restore can consume. Sidecar files (config.json,
// template.xml, volumes.json) are taken from the newest step that has them;
// image.tar (whose compression suffix varies) is located via findArchive.
// Each volume_*.tar is the overlay of every step's archive, oldest first, so
// unchanged files from the base full backup survive a later partial
// differential/incremental archive (issue #320). stepDirs MUST be ordered
// oldest first.
func MergeContainerChainStaging(ctx context.Context, stepDirs []string, outDir string) error {
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("creating merge dir: %w", err)
	}

	// Plain sidecars: newest step wins.
	for _, name := range []string{"config.json", "template.xml", "volumes.json"} {
		for i := len(stepDirs) - 1; i >= 0; i-- {
			src := filepath.Join(stepDirs[i], name)
			if _, err := os.Stat(src); err == nil {
				if err := mergeCopyFile(src, filepath.Join(outDir, name)); err != nil {
					return fmt.Errorf("merge sidecar %s: %w", name, err)
				}
				break
			}
		}
	}

	// image.tar: locate across steps (compression suffix varies).
	for i := len(stepDirs) - 1; i >= 0; i-- {
		if src, err := findArchive(stepDirs[i], "image.tar"); err == nil {
			if err := mergeCopyFile(src, filepath.Join(outDir, filepath.Base(src))); err != nil {
				return fmt.Errorf("merge image archive: %w", err)
			}
			break
		}
	}

	// Group each step's volume archives by the VOLUME they hold rather than by
	// the filename they happen to carry. A chain can span the #352 naming
	// change: the base full may be index-named (volume_0.tar) while a later
	// differential is stable-key named (volume_<hash>.tar). Grouping by raw
	// filename would leave those two as separate archives, so restore would
	// pick the differential alone and drop every unchanged file the base
	// full held — the issue #320 data-loss class, reintroduced across an
	// upgrade. Each step's volumes.json maps its archive names to mount
	// sources, which is the identity both naming schemes share.
	perStep := make([]map[string]string, len(stepDirs)) // canonical base -> archive base in that step
	canonical := map[string]struct{}{}
	for i, dir := range stepDirs {
		byCanonical, err := canonicalVolumeArchives(dir)
		if err != nil {
			return err
		}
		perStep[i] = byCanonical
		for base := range byCanonical {
			canonical[base] = struct{}{}
		}
	}

	sorted := make([]string, 0, len(canonical))
	for b := range canonical {
		sorted = append(sorted, b)
	}
	sort.Strings(sorted)

	for _, base := range sorted {
		work := filepath.Join(outDir, base+".merge")
		if err := os.MkdirAll(work, 0o750); err != nil {
			return fmt.Errorf("creating work dir for %s: %w", base, err)
		}
		// Overlay each step's archive, oldest first. A step that did not
		// archive this volume (unchanged) simply has no matching archive.
		for i, dir := range stepDirs {
			stepBase, ok := perStep[i][base]
			if !ok {
				continue
			}
			archive, err := findArchive(dir, stepBase)
			if err != nil {
				continue
			}
			if err := untarDirectory(ctx, archive, work); err != nil {
				return fmt.Errorf("extracting %s from %s: %w", stepBase, dir, err)
			}
		}
		// Re-tar the merged tree as a plain archive; findArchive checks the
		// plain name first and untarDirectory auto-detects compression.
		if err := tarDirectory(ctx, work, filepath.Join(outDir, base), nil, CompressionNone); err != nil {
			return fmt.Errorf("creating merged %s: %w", base, err)
		}
		if err := os.RemoveAll(work); err != nil {
			return fmt.Errorf("removing work dir for %s: %w", base, err)
		}
	}
	return nil
}

// canonicalVolumeArchives maps each volume archive present in a chain step to
// the canonical name for the volume it holds, keyed canonical -> as-found.
//
// The canonical name is derived from the mount source recorded in the step's
// volumes.json, so an index-named archive and a stable-key-named archive for
// the same volume land on the same key and get overlaid. A step with no
// manifest (or an archive the manifest does not describe) keeps its own name,
// which preserves the previous filename-only behaviour for that archive. An
// unreadable step yields no archives, matching the merge's long-standing
// skip-the-step contract.
func canonicalVolumeArchives(stepDir string) (map[string]string, error) {
	sourceByArchive := map[string]string{}
	if data, err := os.ReadFile(filepath.Join(stepDir, "volumes.json")); err == nil { // #nosec G304 — stepDir is a vault-controlled staging directory
		// A manifest that exists but will not parse must stop the merge.
		// Falling back to filename-only grouping here is what silently drops
		// a base full's data when the chain spans the #352 naming change.
		var manifest []volumeManifestEntry
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, fmt.Errorf("parsing volumes.json in chain step %s: %w", stepDir, err)
		}
		for _, me := range manifest {
			if me.Archive == "" || me.Source == "" {
				continue
			}
			sourceByArchive[TarBaseName(me.Archive)] = me.Source
		}
	}

	entries, err := os.ReadDir(stepDir)
	if err != nil {
		// Preserve the long-standing contract that an unreadable step is
		// skipped rather than failing the whole merge; the readable steps
		// still merge.
		return map[string]string{}, nil
	}

	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "volume_") {
			continue
		}
		base := TarBaseName(e.Name())
		canonicalBase := base
		if source, ok := sourceByArchive[base]; ok {
			canonicalBase = volumeArchiveBase(source)
		}
		out[canonicalBase] = base
	}
	return out, nil
}

// TarBaseName strips any compression suffix, leaving the ".tar" base that
// findArchive probes.
func TarBaseName(name string) string {
	if i := strings.Index(name, ".tar"); i >= 0 {
		return name[:i] + ".tar"
	}
	return name
}

func mergeCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Preserve the source's permission bits (e.g. config.json at 0600) instead
	// of os.Create's 0666&umask default.
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

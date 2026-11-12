package runner

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
)

// selectRestoreObjects picks the storage objects to download. Local names drop
// transport suffixes, so x.tar.gz can land on x.tar: when the restore point
// recorded checksums, an object it never recorded is left out and returned for
// reporting. It is skipped rather than fatal so a stray file such as .DS_Store
// cannot make a good backup unrestorable. Two objects that would share a local
// name are always an error.
func selectRestoreObjects(files []storage.FileInfo, expected map[string]string) ([]storage.FileInfo, []string, error) {
	selected := make([]storage.FileInfo, 0, len(files))
	var unlisted []string
	byLocalName := make(map[string]string, len(files))
	for _, fi := range files {
		if fi.IsDir {
			continue
		}
		name := filepath.Base(fi.Path)
		if len(expected) > 0 {
			if _, listed := expected[name]; !listed {
				unlisted = append(unlisted, name)
				continue
			}
		}
		local := localRestoreName(name)
		if other, clash := byLocalName[local]; clash {
			return nil, nil, fmt.Errorf("objects %q and %q would both restore to %q", other, name, local)
		}
		byLocalName[local] = name
		selected = append(selected, fi)
	}
	return selected, unlisted, nil
}

// localRestoreName strips the suffixes the download pipeline may remove.
func localRestoreName(name string) string {
	name = strings.TrimSuffix(name, ".age")
	for _, suffix := range []string{".gz", ".zst"} {
		if trimmed, ok := strings.CutSuffix(name, suffix); ok {
			return trimmed
		}
	}
	return name
}

// restorePointProvenanceLocal reports that this server made the restore point
// itself. Imported rows take their metadata, checksums included, from the backup's
// own manifest, and replicas from a peer, so neither can vouch for anything.
func (r *Runner) restorePointProvenanceLocal(rp db.RestorePoint) bool {
	var meta struct {
		Imported bool `json:"imported"`
	}
	if err := json.Unmarshal([]byte(rp.Metadata), &meta); err != nil || meta.Imported || rp.SourceID != 0 {
		return false
	}
	job, err := r.db.GetJob(rp.JobID)
	return err == nil && job.SourceID == 0
}

// restorePointVouched reports whether a restore may grant the host access its
// container config asks for without approval: this server made the backup.
// Verification is not required, so a server that runs with Verify backup off
// restores its own privileged containers as before.
func (r *Runner) restorePointVouched(rp db.RestorePoint, _ string) bool {
	return r.restorePointProvenanceLocal(rp)
}

// chainProvenanceLocal reports whether this server made every chain step that
// holds the item; a chain replay can take its config from any of them.
func (r *Runner) chainProvenanceLocal(chain []db.RestorePoint, itemName string) bool {
	for _, rp := range chain {
		if rpContainsItem(rp, itemName) && !r.restorePointProvenanceLocal(rp) {
			return false
		}
	}
	return true
}

// chainSidecarsVouched reports whether every step's sidecars were checked against
// locally recorded checksums. Only then may they decide what a chain prune deletes.
func (r *Runner) chainSidecarsVouched(chain []db.RestorePoint, itemName string) bool {
	for _, rp := range chain {
		if !rpContainsItem(rp, itemName) {
			continue
		}
		if !r.restorePointProvenanceLocal(rp) || len(parseItemChecksums(rp.Metadata, itemName)) == 0 {
			return false
		}
	}
	return true
}

// pruneVouchedContainerChain runs the container chain prune only when its file
// lists are vouched for; storage sidecars alone must not decide what is deleted.
func (r *Runner) pruneVouchedContainerChain(chain []db.RestorePoint, itemName string, stepDirs []string, mergedDir, destination string, reporter restoreProgressReporter) {
	if !r.chainSidecarsVouched(chain, itemName) {
		r.runLog(reporter.RunID, runLogLevelWarn,
			fmt.Sprintf("Kept all restored files for %s: this backup has no stored checksums to vouch for its file lists, so files deleted after the first backup in the chain may have come back", itemName), nil)
		return
	}
	r.pruneContainerChainResurrected(stepDirs, mergedDir, destination, chain[len(chain)-1].ID)
}

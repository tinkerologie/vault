package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"

	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/docsmeta"
	"github.com/tinkerologie/vault/internal/fsstat"
	"github.com/tinkerologie/vault/internal/safepath"
	"github.com/tinkerologie/vault/internal/storage"
	"github.com/tinkerologie/vault/internal/tempdir"
)

// PreflightCheck is a single pre-restore validation result.
type PreflightCheck struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"` // "ok" | "fail" | "warn" | "skip"
	Detail string `json:"detail,omitempty"`
}

// PreflightResult is the outcome of validating a restore before it runs. OK is
// true when no blocking check failed (warnings and skips do not block).
type PreflightResult struct {
	OK     bool             `json:"ok"`
	Checks []PreflightCheck `json:"checks"`
}

// PreflightRestoreOptions describes the selected restore unit. An empty Items
// slice requests the legacy whole-backup estimate.
type PreflightRestoreOptions struct {
	Passphrase       string
	Destination      string
	Items            []string
	ItemTypes        map[string]string
	DestinationIsZFS bool
}

// freeSpaceAt returns the bytes available at path. Overridable in tests.
var freeSpaceAt = func(path string) (int64, error) {
	u, err := fsstat.Stat(path)
	if err != nil {
		return 0, err
	}
	return int64(u.Free), nil //nolint:gosec // filesystem sizes fit in int64
}

// PreflightRestore validates that a restore is likely to succeed before it is
// started: the destination is reachable, the restore point is present on
// storage, an encrypted backup's passphrase is correct, and there is enough
// free space to stage the data. It performs only cheap probes (no full data
// read) so it stays interactive. Verifying a backup's bytes end-to-end is the
// separate, heavier VerifyRestorePoint operation.
func (r *Runner) PreflightRestore(job db.Job, rp db.RestorePoint, opts PreflightRestoreOptions) PreflightResult {
	var checks []PreflightCheck
	blocked := false
	add := func(c PreflightCheck) {
		if c.Status == "fail" {
			blocked = true
		}
		checks = append(checks, c)
	}

	dest, err := r.db.GetStorageDestination(job.StorageDestID)
	if err != nil {
		add(PreflightCheck{ID: "reachable", Label: "Storage reachable", Status: "fail", Detail: "storage destination not found"})
		return PreflightResult{OK: false, Checks: checks}
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		add(PreflightCheck{ID: "reachable", Label: "Storage reachable", Status: "fail", Detail: err.Error()})
		return PreflightResult{OK: false, Checks: checks}
	}
	defer storage.CloseAdapter(adapter)

	if err := adapter.TestConnection(); err != nil {
		add(PreflightCheck{ID: "reachable", Label: "Storage reachable", Status: "fail", Detail: err.Error()})
		return PreflightResult{OK: false, Checks: checks}
	}
	add(PreflightCheck{ID: "reachable", Label: "Storage reachable", Status: "ok", Detail: dest.Name})

	add(checkRestorePointPresent(adapter, dest, rp))
	add(r.checkDecryptable(job, opts.Passphrase))

	requirement := estimateRestoreSize(rp, opts.Items)
	stagingItems, staged, archiveSizes := preflightStagingNeeds(rp, opts)
	requirement.archiveSizes = archiveSizes
	staging := requirement
	if staged && len(stagingItems) < len(opts.Items) {
		staging = estimateRestoreSize(rp, stagingItems)
		staging.archiveSizes = archiveSizes
		if staging.scope == "selected items" {
			staging.scope = "selected items that use staging"
		}
	}
	stage := PreflightCheck{ID: "space", Label: "Restore staging space", Status: "skip", Detail: "The selected Backup Items restore directly from deduplicated storage."}
	var stagePath string
	var stageFree int64
	if len(opts.Items) == 0 || staged {
		override, _ := r.db.GetSetting("staging_dir_override", docsmeta.DefaultFor("staging_dir_override"))
		path, source := tempdir.PreviewRestoreStaging(override)
		free, err := freeSpaceAt(path)
		if err == nil {
			stagePath, stageFree = path, free
		}
		stage = capacityCheck("space", "Restore staging space", path, source, free, err, staging)
		if rp.BackupType == "incremental" || rp.BackupType == "differential" {
			stage.Detail += " Earlier Backup Chain steps are also staged and are outside this estimate."
		}
	}
	if opts.Destination == "" {
		stage.Detail += " Original-location targets, including VM disk and NVRAM paths, are unchecked."
	}
	add(stage)

	if opts.Destination != "" {
		if opts.DestinationIsZFS {
			add(PreflightCheck{ID: "destination_space", Label: "Restore destination space", Status: "skip", Detail: "ZFS dataset target. Filesystem free space is unchecked."})
		} else {
			add(checkDestinationSpace(opts.Destination, stagePath, stageFree, staged, requirement, staging))
		}
	}

	return PreflightResult{OK: !blocked, Checks: checks}
}

// checkRestorePointPresent confirms the backup's data still exists on storage.
// For dedup destinations the data lives in the shared repo, so the repo config
// is the meaningful probe; otherwise the restore point's own directory must
// contain files.
func checkRestorePointPresent(adapter storage.Adapter, dest db.StorageDestination, rp db.RestorePoint) PreflightCheck {
	c := PreflightCheck{ID: "present", Label: "Backup present on storage"}
	if dest.DedupEnabled {
		if _, err := adapter.Stat(dedup.RepoRoot + "/repo.json"); err != nil {
			if storage.IsNotExist(err) {
				c.Status, c.Detail = "fail", "deduplication repository is missing"
			} else {
				c.Status, c.Detail = "warn", err.Error()
			}
			return c
		}
		c.Status, c.Detail = "ok", "deduplication repository found"
		return c
	}
	if rp.StoragePath == "" {
		c.Status, c.Detail = "skip", "no storage path recorded for this restore point"
		return c
	}
	files, err := adapter.List(rp.StoragePath)
	if err != nil {
		if storage.IsNotExist(err) {
			c.Status, c.Detail = "fail", "backup files were not found on storage"
		} else {
			c.Status, c.Detail = "warn", err.Error()
		}
		return c
	}
	if len(files) == 0 {
		c.Status, c.Detail = "fail", "the backup directory is empty"
		return c
	}
	c.Status, c.Detail = "ok", fmt.Sprintf("%d file%s", len(files), plural(len(files)))
	return c
}

// checkDecryptable verifies the supplied passphrase against the configured
// hash for age-encrypted backups. Unencrypted backups skip the check.
func (r *Runner) checkDecryptable(job db.Job, passphrase string) PreflightCheck {
	c := PreflightCheck{ID: "decryptable", Label: "Decryptable"}
	if job.Encryption != "age" {
		c.Status, c.Detail = "skip", "backup is not encrypted"
		return c
	}
	if passphrase == "" {
		c.Status, c.Detail = "fail", "this backup is encrypted — a passphrase is required"
		return c
	}
	hash, err := r.db.GetSetting("encryption_passphrase_hash", docsmeta.DefaultFor("encryption_passphrase_hash"))
	if err != nil {
		c.Status, c.Detail = "warn", fmt.Sprintf("could not read the stored passphrase hash: %v", err)
		return c
	}
	if hash == "" {
		c.Status, c.Detail = "warn", "no stored passphrase to verify against"
		return c
	}
	if err := crypto.VerifyPassphrase(passphrase, hash); err != nil {
		c.Status, c.Detail = "fail", "passphrase does not match"
		return c
	}
	c.Status, c.Detail = "ok", "passphrase verified"
	return c
}

type restoreSizeEstimate struct {
	bytes        int64
	scope        string
	known        bool
	archiveSizes bool
}

// Classic VM and ZFS artifacts are staged before transport compression. Classic
// container, folder and plugin backups can record engine-compressed archives.
// Per-item manifests distinguish direct dedup restores in mixed restore points.
func preflightStagingNeeds(rp db.RestorePoint, opts PreflightRestoreOptions) (items []string, staged, archiveSizes bool) {
	var meta struct {
		Manifests map[string]string `json:"item_manifests"`
	}
	if rp.Metadata != "" {
		_ = json.Unmarshal([]byte(rp.Metadata), &meta)
	}
	if len(opts.Items) == 0 {
		return nil, len(meta.Manifests) == 0 && len(rp.ManifestID) == 0, false
	}
	for _, item := range opts.Items {
		if meta.Manifests[item] != "" || (meta.Manifests == nil && len(rp.ManifestID) != 0) {
			continue
		}
		items = append(items, item)
		switch opts.ItemTypes[item] {
		case "container", "folder", "plugin":
			archiveSizes = true
		}
	}
	return items, len(items) > 0, archiveSizes
}

func estimateRestoreSize(rp db.RestorePoint, items []string) restoreSizeEstimate {
	whole := restoreSizeEstimate{bytes: rp.SizeBytes, scope: "whole backup", known: rp.SizeBytes > 0}
	if len(items) == 0 {
		return whole
	}
	sizes, _ := rp.ItemSizes()
	seen := make(map[string]struct{}, len(items))
	var total int64
	for _, item := range items {
		if _, duplicate := seen[item]; duplicate {
			continue
		}
		size, known := sizes[item]
		if !known || size < 0 || size > math.MaxInt64-total {
			return whole
		}
		seen[item] = struct{}{}
		total += size
	}
	return restoreSizeEstimate{bytes: total, scope: "selected items", known: true}
}

// probeRestoreDestination walks up only when the requested path is missing.
// Permission and other probe errors must not be mistaken for available space
// on a parent filesystem.
func probeRestoreDestination(destination string) (string, int64, error) {
	path, validationErr := safepath.CheckCustomRestoreDestination(destination)
	if validationErr != nil {
		return destination, 0, validationErr
	}
	for {
		free, err := freeSpaceAt(path)
		parent := filepath.Dir(path)
		if !errors.Is(err, fs.ErrNotExist) || parent == path {
			return path, free, err
		}
		// Statfs also returns ENOENT for a dangling symlink. Its parent
		// filesystem doesn't describe where the restore would write.
		if _, lstatErr := safepath.LstatPinned(path); !errors.Is(lstatErr, fs.ErrNotExist) {
			return path, free, err
		}
		path = parent
	}
}

func checkDestinationSpace(destination, stagePath string, stageFree int64, includeStaging bool, estimate, staging restoreSizeEstimate) PreflightCheck {
	path, free, err := probeRestoreDestination(destination)
	var shared bool
	var deviceErr error
	if err == nil && includeStaging && stagePath != "" {
		shared, deviceErr = fsstat.SameFilesystem(stagePath, path)
	}
	if shared {
		free = min(free, stageFree)
		// Only classic items hold a staging allocation while restoring.
		if !estimate.known || !staging.known || staging.bytes > math.MaxInt64-estimate.bytes {
			estimate.known = false
		} else {
			estimate.bytes += staging.bytes
		}
	}
	check := capacityCheck("destination_space", "Restore destination space", path, "destination", free, err, estimate)
	if path != destination {
		check.Detail += fmt.Sprintf(" Probed the nearest existing ancestor for %s.", destination)
	}
	if shared {
		check.Detail += " Staging and the destination share this filesystem. The estimate includes the staged copy of items that use staging."
	}
	if deviceErr != nil {
		if check.Status == "ok" {
			check.Status = "skip"
		}
		check.Detail += " Could not determine whether staging shares this filesystem."
	}
	return check
}

func capacityCheck(id, label, path, source string, free int64, err error, estimate restoreSizeEstimate) PreflightCheck {
	c := PreflightCheck{ID: id, Label: label}
	requirement := "unknown"
	if estimate.known {
		requirement = humanBytes(estimate.bytes)
	}
	detail := fmt.Sprintf("Estimated space for the %s is %s.", estimate.scope, requirement)
	if estimate.archiveSizes {
		detail += " Expanded archive sizes are unavailable. Recorded sizes may understate required space."
	}
	if err != nil {
		c.Status, c.Detail = "skip", fmt.Sprintf("Could not determine free space at %s (%s). %s", path, source, detail)
		return c
	}
	c.Detail = fmt.Sprintf("%s free at %s (%s). %s", humanBytes(free), path, source, detail)
	switch {
	case !estimate.known || estimate.bytes <= 0:
		c.Status = "skip"
	case free < estimate.bytes:
		c.Status = "warn"
	case estimate.archiveSizes:
		c.Status = "skip"
	default:
		c.Status = "ok"
	}
	return c
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

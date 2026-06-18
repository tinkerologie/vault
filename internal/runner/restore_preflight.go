package runner

import (
	"fmt"
	"os"
	"syscall"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
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

// freeSpaceAt returns the bytes available at path. Overridable in tests.
// syscall.Statfs is available on both Linux (the daemon's target) and macOS
// (the dev/test platform); Bavail/Bsize exist on both.
var freeSpaceAt = func(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:unconvert,gosec // cross-platform field widths
}

// PreflightRestore validates that a restore is likely to succeed before it is
// started: the destination is reachable, the restore point is present on
// storage, an encrypted backup's passphrase is correct, and there is enough
// free space to stage the data. It performs only cheap probes (no full data
// read) so it stays interactive. Verifying a backup's bytes end-to-end is the
// separate, heavier VerifyRestorePoint operation.
func (r *Runner) PreflightRestore(job db.Job, rp db.RestorePoint, passphrase, destination string) PreflightResult {
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
	} else {
		add(PreflightCheck{ID: "reachable", Label: "Storage reachable", Status: "ok", Detail: dest.Name})
	}

	add(checkRestorePointPresent(adapter, dest, rp))
	add(r.checkDecryptable(job, passphrase))
	add(r.checkFreeSpace(destination, rp))

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
	hash, err := r.db.GetSetting("encryption_passphrase_hash", "")
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

// checkFreeSpace is a best-effort, non-blocking check that the restore target
// (the custom destination if given, otherwise the staging directory) has room
// for the backup. Indeterminate targets are skipped rather than failed.
func (r *Runner) checkFreeSpace(destination string, rp db.RestorePoint) PreflightCheck {
	c := PreflightCheck{ID: "space", Label: "Free space"}
	target := destination
	if target == "" {
		// The staging override is optional; if it can't be read (missing or a
		// settings error) fall back to the temp dir, which is still a valid
		// target to probe for free space.
		if override, err := r.db.GetSetting("staging_dir_override", ""); err == nil && override != "" {
			target = override
		} else {
			target = os.TempDir()
		}
	}
	free, err := freeSpaceAt(target)
	if err != nil {
		c.Status, c.Detail = "skip", "could not determine free space at the target"
		return c
	}
	if rp.SizeBytes > 0 && free < rp.SizeBytes {
		c.Status = "warn"
		c.Detail = fmt.Sprintf("%s free, but the backup is %s", humanBytes(free), humanBytes(rp.SizeBytes))
		return c
	}
	c.Status, c.Detail = "ok", fmt.Sprintf("%s free", humanBytes(free))
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

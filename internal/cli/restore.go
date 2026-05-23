package cli

import (
	"log"
	"os"

	"github.com/ruaan-deysel/vault/internal/db"
)

// restoreWithFallback attempts to restore the database from a chain of
// snapshot sources. It tries each source in order and returns information
// about which source was used (or that a fresh database was started).
//
// Fallback chain:
//  1. Configured snapshot path (from vault.cfg SNAPSHOT_PATH)
//  2. Default cache path (/mnt/cache/.vault/vault.db)
//  3. USB backup (/boot/config/plugins/vault/vault.db.backup)
//  4. Fresh database (no restoration)
func restoreWithFallback(sm *db.SnapshotManager, configuredPath, defaultCachePath, usbBackupPath string) *db.RestorationInfo {
	tryTier := func(label, path, reason string) *db.RestorationInfo {
		if path == "" {
			return nil
		}
		if _, err := os.Stat(path); err != nil {
			return nil
		}
		if err := sm.RestoreFromPath(path); err != nil {
			log.Printf("Warning: failed to restore from %s path %s: %v", label, path, err)
			return nil
		}
		if err := sm.IntegrityCheck(); err != nil {
			log.Printf("Warning: %s snapshot %s failed integrity check: %v", label, path, err)
			return nil
		}
		return &db.RestorationInfo{
			Source: label,
			Path:   path,
			Reason: reason,
		}
	}

	if info := tryTier("primary", configuredPath, "restored from configured snapshot path"); info != nil {
		return info
	}
	if defaultCachePath != configuredPath {
		if info := tryTier("default_cache", defaultCachePath, "restored from default cache snapshot (configured path unavailable or invalid)"); info != nil {
			return info
		}
	}
	if info := tryTier("usb_backup", usbBackupPath, "restored from USB flash backup (other snapshots unavailable or invalid)"); info != nil {
		return info
	}
	log.Println("Warning: no valid snapshot source available — starting with fresh database")
	return &db.RestorationInfo{
		Source: "fresh",
		Path:   "",
		Reason: "no snapshot files passed integrity check; configuration will need to be reconfigured",
	}
}

// validateConfiguredPaths checks that user-configured paths (snapshot override,
// staging override) are accessible and logs warnings for any that are not.
func validateConfiguredPaths(database *db.DB) {
	if snapOverride, err := database.GetSetting("snapshot_path_override", ""); err == nil && snapOverride != "" {
		if _, err := os.Stat(snapOverride); err != nil {
			log.Printf("Warning: configured snapshot_path_override is not accessible: %s (%v)", snapOverride, err)
		}
	}

	if stagingOverride, err := database.GetSetting("staging_dir_override", ""); err == nil && stagingOverride != "" {
		if fi, err := os.Stat(stagingOverride); err != nil || !fi.IsDir() {
			log.Printf("Warning: configured staging_dir_override is not accessible: %s", stagingOverride)
		}
	}
}

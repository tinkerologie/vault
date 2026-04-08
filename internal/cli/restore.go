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
	// 1. Try the configured snapshot path (primary).
	if configuredPath != "" {
		if _, err := os.Stat(configuredPath); err == nil {
			if err := sm.RestoreFromPath(configuredPath); err == nil {
				return &db.RestorationInfo{
					Source: "primary",
					Path:   configuredPath,
					Reason: "restored from configured snapshot path",
				}
			}
			log.Printf("Warning: failed to restore from configured path %s: %v", configuredPath, err)
		}
	}

	// 2. Try the default cache path (if different from configured).
	if defaultCachePath != configuredPath {
		if _, err := os.Stat(defaultCachePath); err == nil {
			if err := sm.RestoreFromPath(defaultCachePath); err == nil {
				return &db.RestorationInfo{
					Source: "default_cache",
					Path:   defaultCachePath,
					Reason: "restored from default cache snapshot (configured path unavailable)",
				}
			}
			log.Printf("Warning: failed to restore from default cache path %s: %v", defaultCachePath, err)
		}
	}

	// 3. Try the USB backup.
	if _, err := os.Stat(usbBackupPath); err == nil {
		if err := sm.RestoreFromPath(usbBackupPath); err == nil {
			return &db.RestorationInfo{
				Source: "usb_backup",
				Path:   usbBackupPath,
				Reason: "restored from USB flash backup (cache snapshot unavailable)",
			}
		}
		log.Printf("Warning: failed to restore from USB backup %s: %v", usbBackupPath, err)
	}

	// 4. No source available — start fresh.
	log.Println("Warning: no snapshot source available — starting with fresh database")
	return &db.RestorationInfo{
		Source: "fresh",
		Path:   "",
		Reason: "no snapshot files found; configuration will need to be reconfigured",
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

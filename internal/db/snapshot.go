package db

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
)

// SnapshotManager handles SQLite backup/restore between the working database
// and a persistent snapshot file on disk.
type SnapshotManager struct {
	db                  *DB
	snapshotPath        string
	defaultSnapshotPath string // default path used when override is cleared.
	usbBackupPath       string // USB flash shadow copy path (empty = disabled).
	lastSnapshot        time.Time
	lastUSBBackup       time.Time
	usbMinInterval      time.Duration // minimum interval between USB writes.
	restorationInfo     *RestorationInfo
	mu                  sync.Mutex
}

// RestorationInfo records which source was used to restore the database at
// startup. This is used by the health endpoint to report degraded state.
type RestorationInfo struct {
	Source string // "primary", "default_cache", "usb_backup", "fresh"
	Path   string // filesystem path used for restoration
	Reason string // human-readable explanation
}

// NewSnapshotManager creates a SnapshotManager that will save/restore snapshots
// to/from the given path. The defaultPath is used as the fallback when
// SetSnapshotPath is called with an empty string.
func NewSnapshotManager(database *DB, snapshotPath, defaultPath string) *SnapshotManager {
	return &SnapshotManager{
		db:                  database,
		snapshotPath:        snapshotPath,
		defaultSnapshotPath: defaultPath,
		usbMinInterval:      1 * time.Hour,
	}
}

// SetUSBBackupPath enables the USB shadow backup at the given path.
func (sm *SnapshotManager) SetUSBBackupPath(path string) {
	if path != "" {
		validPath, err := validateSnapshotPath(path)
		if err != nil {
			log.Printf("Warning: invalid USB backup path: %v", err)
			return
		}
		path = validPath
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.usbBackupPath = path
}

// SetRestorationInfo records which source was used for DB restoration.
func (sm *SnapshotManager) SetRestorationInfo(info *RestorationInfo) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.restorationInfo = info
}

// RestorationSource returns the restoration info recorded at startup.
func (sm *SnapshotManager) RestorationSource() *RestorationInfo {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.restorationInfo
}

// SnapshotPath returns the persistent snapshot location.
func (sm *SnapshotManager) SnapshotPath() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.snapshotPath
}

// DefaultSnapshotPath returns the default snapshot path set at construction.
func (sm *SnapshotManager) DefaultSnapshotPath() string {
	return sm.defaultSnapshotPath
}

// validateSnapshotPath rejects input containing path traversal components and
// returns the cleaned absolute path.
func validateSnapshotPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("snapshot path must not be empty")
	}

	// Reject ".." components BEFORE cleaning — filepath.Clean would silently
	// normalise them away, defeating traversal detection.  Use forward-slash
	// splitting so the check works regardless of OS path separator.
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return "", fmt.Errorf("path traversal not allowed in snapshot path")
		}
	}

	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve snapshot path: %w", err)
	}

	return absPath, nil
}

// SetSnapshotPath changes the snapshot path at runtime and immediately saves
// a fresh snapshot at the new location. If newPath is empty, the default
// snapshot path is used. This ensures the target location has up-to-date data.
func (sm *SnapshotManager) SetSnapshotPath(newPath string) error {
	if newPath == "" {
		newPath = sm.defaultSnapshotPath
	}

	validPath, err := validateSnapshotPath(newPath)
	if err != nil {
		return fmt.Errorf("validating snapshot path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(validPath), 0o750); err != nil {
		return fmt.Errorf("creating snapshot directory: %w", err)
	}

	sm.mu.Lock()
	sm.snapshotPath = validPath
	sm.mu.Unlock()

	// Save a fresh snapshot to the new location so it is never stale.
	if err := sm.SaveSnapshot(); err != nil {
		return fmt.Errorf("saving initial snapshot at new path: %w", err)
	}
	return nil
}

// LastSnapshot returns the time of the last successful snapshot (mutex-protected).
func (sm *SnapshotManager) LastSnapshot() time.Time {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.lastSnapshot
}

// SaveSnapshot copies the working DB to the snapshot path using the SQLite
// backup API.
func (sm *SnapshotManager) SaveSnapshot() error {
	sm.mu.Lock()
	snapshotPath := sm.snapshotPath
	sm.mu.Unlock()

	validPath, err := validateSnapshotPath(snapshotPath)
	if err != nil {
		return fmt.Errorf("validating snapshot path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(validPath), 0o750); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}

	conn, err := sm.db.DB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	err = conn.Raw(func(driverConn any) error {
		type backuper interface {
			NewBackup(string) (*sqlite.Backup, error)
		}
		bck, err := driverConn.(backuper).NewBackup(validPath)
		if err != nil {
			return err
		}
		for more := true; more; {
			more, err = bck.Step(-1)
			if err != nil {
				return err
			}
		}
		return bck.Finish()
	})
	if err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}

	sm.mu.Lock()
	sm.lastSnapshot = time.Now()
	sm.mu.Unlock()
	return nil
}

// RestoreFromSnapshot copies the snapshot file into the working DB using the
// SQLite restore API. If the snapshot file does not exist, it logs a message
// and returns nil (no-op for first run).
func (sm *SnapshotManager) RestoreFromSnapshot() error {
	sm.mu.Lock()
	snapshotPath := sm.snapshotPath
	sm.mu.Unlock()

	validPath, err := validateSnapshotPath(snapshotPath)
	if err != nil {
		return fmt.Errorf("validating snapshot path: %w", err)
	}

	if _, err := os.Stat(validPath); os.IsNotExist(err) {
		log.Printf("no snapshot file at %s, skipping restore", validPath)
		return nil
	}

	conn, err := sm.db.DB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	err = conn.Raw(func(driverConn any) error {
		type restorer interface {
			NewRestore(string) (*sqlite.Backup, error)
		}
		bck, err := driverConn.(restorer).NewRestore(validPath)
		if err != nil {
			return err
		}
		for more := true; more; {
			more, err = bck.Step(-1)
			if err != nil {
				return err
			}
		}
		return bck.Finish()
	})
	if err != nil {
		return fmt.Errorf("restore snapshot: %w", err)
	}

	sm.mu.Lock()
	sm.lastSnapshot = time.Now()
	sm.mu.Unlock()
	return nil
}

// FlushToUSB saves the primary snapshot and immediately forces a USB shadow
// copy regardless of the throttle interval. Call this after configuration
// changes (job/storage/settings CRUD) so the USB flash always has fresh data.
// The USB backup is attempted even if the primary snapshot fails, since the
// USB backup copies directly from the working in-memory DB.
func (sm *SnapshotManager) FlushToUSB() error {
	snapErr := sm.SaveSnapshot()
	sm.saveUSBBackup(true)
	return snapErr
}

// Close performs a final snapshot save as a flush, including a USB backup.
func (sm *SnapshotManager) Close() error {
	return sm.FlushToUSB()
}

// SaveSnapshotAndUSBBackup saves the primary snapshot and, if enough time has
// passed, also writes a throttled USB shadow copy.
// The USB backup is attempted even if the primary snapshot fails.
func (sm *SnapshotManager) SaveSnapshotAndUSBBackup() error {
	snapErr := sm.SaveSnapshot()
	sm.saveUSBBackup(false)
	return snapErr
}

// saveUSBBackup writes a shadow copy of the working DB to USB flash.
// If force is false, the write is throttled to usbMinInterval to reduce flash wear.
func (sm *SnapshotManager) saveUSBBackup(force bool) {
	sm.mu.Lock()
	usbPath := sm.usbBackupPath
	lastBackup := sm.lastUSBBackup
	interval := sm.usbMinInterval
	sm.mu.Unlock()

	if usbPath == "" {
		return
	}

	validPath, err := validateSnapshotPath(usbPath)
	if err != nil {
		log.Printf("Warning: invalid USB backup path: %v", err)
		return
	}

	if !force && !lastBackup.IsZero() && time.Since(lastBackup) < interval {
		return
	}

	if err := os.MkdirAll(filepath.Dir(validPath), 0o750); err != nil {
		log.Printf("Warning: failed to create USB backup directory: %v", err)
		return
	}

	conn, err := sm.db.DB.Conn(context.Background())
	if err != nil {
		log.Printf("Warning: failed to acquire connection for USB backup: %v", err)
		return
	}
	defer conn.Close()

	err = conn.Raw(func(driverConn any) error {
		type backuper interface {
			NewBackup(string) (*sqlite.Backup, error)
		}
		bck, err := driverConn.(backuper).NewBackup(validPath)
		if err != nil {
			return err
		}
		for more := true; more; {
			more, err = bck.Step(-1)
			if err != nil {
				return err
			}
		}
		return bck.Finish()
	})
	if err != nil {
		log.Printf("Warning: USB backup failed: %v", err)
		return
	}

	sm.mu.Lock()
	sm.lastUSBBackup = time.Now()
	sm.mu.Unlock()
	log.Printf("USB shadow backup saved to %s", validPath)
}

// RestoreFromPath restores the database from the specified source path.
// Returns an error if the file doesn't exist or restoration fails.
func (sm *SnapshotManager) RestoreFromPath(sourcePath string) error {
	validPath, err := validateSnapshotPath(sourcePath)
	if err != nil {
		return fmt.Errorf("validating source path: %w", err)
	}

	if _, err := os.Stat(validPath); os.IsNotExist(err) {
		return fmt.Errorf("snapshot file does not exist: %s", validPath)
	}

	conn, err := sm.db.DB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	err = conn.Raw(func(driverConn any) error {
		type restorer interface {
			NewRestore(string) (*sqlite.Backup, error)
		}
		bck, err := driverConn.(restorer).NewRestore(validPath)
		if err != nil {
			return err
		}
		for more := true; more; {
			more, err = bck.Step(-1)
			if err != nil {
				return err
			}
		}
		return bck.Finish()
	})
	if err != nil {
		return fmt.Errorf("restore from %s: %w", validPath, err)
	}

	sm.mu.Lock()
	sm.lastSnapshot = time.Now()
	sm.mu.Unlock()
	return nil
}

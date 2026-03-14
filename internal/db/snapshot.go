package db

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"modernc.org/sqlite"
)

// SnapshotManager handles SQLite backup/restore between the working database
// and a persistent snapshot file on disk.
type SnapshotManager struct {
	db           *DB
	snapshotPath string
	lastSnapshot time.Time
	mu           sync.Mutex
}

// NewSnapshotManager creates a SnapshotManager that will save/restore snapshots
// to/from the given path.
func NewSnapshotManager(database *DB, snapshotPath string) *SnapshotManager {
	return &SnapshotManager{
		db:           database,
		snapshotPath: snapshotPath,
	}
}

// SnapshotPath returns the persistent snapshot location.
func (sm *SnapshotManager) SnapshotPath() string {
	return sm.snapshotPath
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
	if err := os.MkdirAll(filepath.Dir(sm.snapshotPath), 0o755); err != nil {
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
		bck, err := driverConn.(backuper).NewBackup(sm.snapshotPath)
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
	if _, err := os.Stat(sm.snapshotPath); os.IsNotExist(err) {
		log.Printf("no snapshot file at %s, skipping restore", sm.snapshotPath)
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
		bck, err := driverConn.(restorer).NewRestore(sm.snapshotPath)
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

// Close performs a final snapshot save as a flush.
func (sm *SnapshotManager) Close() error {
	return sm.SaveSnapshot()
}

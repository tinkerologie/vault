package db

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
)

// BackupToFile writes a standalone SQLite snapshot to a new file in a
// caller-owned private directory. The caller owns publication and cleanup.
// It holds the pooled connection until the copy finishes. Cancellation is
// checked while acquiring the connection and between batches of pages.
func (d *DB) BackupToFile(ctx context.Context, dest string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if dest == "" {
		return fmt.Errorf("database snapshot path is empty")
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("resolving database snapshot path: %w", err)
	}
	live, err := filepath.Abs(d.Path())
	if err != nil {
		return fmt.Errorf("resolving live database path: %w", err)
	}
	if dest == live {
		return fmt.Errorf("database snapshot path is the live database")
	}

	// Reserve a private file without following an existing file or link.
	file, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- caller-owned private directory
	if err != nil {
		return fmt.Errorf("creating database snapshot file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(dest)
		}
	}()
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing reserved snapshot file: %w", err)
	}

	// Escape URI delimiters in filenames so SQLite opens the reserved file.
	path := filepath.ToSlash(dest)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	const pagesPerStep = 256
	if err := d.onlineBackup(ctx, uri.String(), pagesPerStep); err != nil {
		return fmt.Errorf("creating database snapshot: %w", err)
	}
	return nil
}

func (d *DB) onlineBackup(ctx context.Context, dest string, pagesPerStep int32) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	return conn.Raw(func(driverConn any) (err error) {
		if err := ctx.Err(); err != nil {
			return err
		}
		type backuper interface {
			NewBackup(string) (*sqlite.Backup, error)
		}
		driver, ok := driverConn.(backuper)
		if !ok {
			return fmt.Errorf("driver connection does not support online backup")
		}
		backup, err := driver.NewBackup(dest)
		if err != nil {
			return fmt.Errorf("init backup: %w", err)
		}
		defer func() {
			if finishErr := backup.Finish(); err == nil {
				err = finishErr
			}
		}()
		return copyOnlineBackupPages(ctx, pagesPerStep, backup.Step)
	})
}

func copyOnlineBackupPages(ctx context.Context, pagesPerStep int32, step func(int32) (bool, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		more, err := step(pagesPerStep)
		if err != nil || !more {
			return err
		}
	}
}

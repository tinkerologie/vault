//go:build windows

package maintlock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// The Windows build ships `vault dedup` for disaster recovery. os.TempDir is
// per-user there, as with any per-user install of the CLI.
func defaultRuntimeDir() string { return os.TempDir() }

func tryLock(f *os.File, exclusive bool) error {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return ErrBusy
	}
	if err != nil {
		return fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return nil
}

//go:build unix

package maintlock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func tryLock(f *os.File, exclusive bool) error {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB) // #nosec G115 -- a file descriptor fits in int
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrBusy
	}
	if err != nil {
		return fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return nil
}

//go:build !unix && !windows

package maintlock

import (
	"fmt"
	"os"
)

func defaultRuntimeDir() string { return os.TempDir() }

// tryLock fails closed on platforms with no file locking support here.
func tryLock(f *os.File, _ bool) error {
	return fmt.Errorf("lock %s: repository maintenance is not supported on this platform", f.Name())
}

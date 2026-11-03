// Package maintlock coordinates destructive maintenance with the writers it
// could race. It has two independent layers:
//
//   - A host-wide lock file. A running daemon or replica holds it shared for
//     its whole lifetime, and `vault dedup gc` and `vault dedup repair` take it
//     exclusively, so maintenance from the CLI never runs alongside a process
//     that can be writing to a repository.
//   - An in-process lock per destination. Imports and replication syncs hold it
//     shared while they turn files into restore points; deleting orphans takes
//     it exclusively, so a file cannot be deleted while it is being adopted.
package maintlock

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrBusy reports that another Vault process holds the host lock.
var ErrBusy = errors.New("another Vault process is using this host's backup repositories")

const lockFileName = "vault-maintenance.lock"

// Test seams.
var (
	runtimeDir       = defaultRuntimeDir()
	sharedRetryEvery = time.Second
)

// SetDirForTest points the host lock at dir, so tests never contend with a
// real daemon or with another test binary.
func SetDirForTest(dir string) { runtimeDir = dir }

// lockPath returns the one lock file every process on the host shares, whoever
// it runs as. The replica image creates it in advance for its unprivileged user.
// It is never deleted: a process that opened it first would lock the old inode
// while the next one locks a new file, and they would no longer exclude each
// other. It lives on tmpfs, so a reboot clears it.
func lockPath() string { return filepath.Join(runtimeDir, lockFileName) }

// HostLock is a held host-wide lock. Release is safe to call more than once.
type HostLock struct {
	once sync.Once
	f    *os.File
}

// Release drops the lock.
func (l *HostLock) Release() {
	l.once.Do(func() { _ = l.f.Close() })
}

func openLock() (*os.File, error) {
	p := lockPath()
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- fixed name under a fixed runtime directory
	if err != nil {
		return nil, fmt.Errorf("open maintenance lock %s: %w", p, err)
	}
	return f, nil
}

// AcquireExclusive takes the host lock for CLI maintenance without waiting.
// It returns ErrBusy while a daemon, replica or other maintenance command runs.
func AcquireExclusive() (*HostLock, error) {
	f, err := openLock()
	if err != nil {
		return nil, err
	}
	if err := tryLock(f, true); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &HostLock{f: f}, nil
}

// AcquireShared takes the host lock for a daemon's lifetime, waiting for any
// running CLI maintenance to finish so no backup starts underneath it. It only
// gives up when ctx ends or the lock cannot be used at all.
func AcquireShared(ctx context.Context) (*HostLock, error) {
	f, err := openLock()
	if err != nil {
		return nil, err
	}
	logged := false
	for {
		err := tryLock(f, false)
		if err == nil {
			return &HostLock{f: f}, nil
		}
		if !errors.Is(err, ErrBusy) {
			_ = f.Close()
			return nil, err
		}
		if !logged {
			log.Println("Waiting for a running vault dedup command to finish before starting")
			logged = true
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(sharedRetryEvery):
		}
	}
}

// destKey scopes a destination's lock to its database, as IDs mean nothing
// across databases.
type destKey struct {
	scope any
	id    int64
}

var (
	destMu    sync.Mutex
	destLocks = map[destKey]*sync.RWMutex{}
)

func destLock(scope any, destID int64) *sync.RWMutex {
	destMu.Lock()
	defer destMu.Unlock()
	k := destKey{scope, destID}
	l, ok := destLocks[k]
	if !ok {
		l = &sync.RWMutex{}
		destLocks[k] = l
	}
	return l
}

// HoldDestination marks an adoption (import or replication sync) in progress
// on destID until the returned function is called. scope is the database handle.
func HoldDestination(scope any, destID int64) (release func()) {
	l := destLock(scope, destID)
	l.RLock()
	return l.RUnlock
}

// TryExclusiveDestination claims destID for deletion, reporting false when an
// adoption is in progress.
func TryExclusiveDestination(scope any, destID int64) (release func(), ok bool) {
	l := destLock(scope, destID)
	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
}

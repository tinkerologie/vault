package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/storage"
)

// preflightTimeout caps how long TestConnection is allowed to take during
// a pre-flight check. Shorter than the daily health-check timeout because
// pre-flight runs in the hot path of a scheduled job — a hung remote
// should fail fast and let retry policy decide whether to try again.
const preflightTimeout = 15 * time.Second

// ErrPreflightFailed is returned when the destination's TestConnection
// returned a non-nil error within the timeout. Wraps the underlying error.
var ErrPreflightFailed = errors.New("preflight: destination unreachable")

// ErrPreflightTimeout is returned when TestConnection did not complete
// within preflightTimeout.
var ErrPreflightTimeout = errors.New("preflight: timed out")

// Preflight verifies a destination is reachable before a backup run starts.
// Constructs a transient adapter, runs TestConnection with a deadline,
// and returns an error wrapping either ErrPreflightFailed or
// ErrPreflightTimeout. On success returns nil.
func Preflight(ctx context.Context, dest db.StorageDestination) error {
	return preflight(ctx, dest, func(check func()) { go check() })
}

func preflight(ctx context.Context, dest db.StorageDestination, launch func(func())) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return fmt.Errorf("%w: adapter construction: %v", ErrPreflightFailed, err)
	}
	return preflightResult(ctx, launchStorageProbe(adapter, launch), preflightTimeout)
}

func preflightResult(ctx context.Context, result <-chan error, timeout time.Duration) error {
	select {
	case err := <-result:
		if err != nil {
			return fmt.Errorf("%w: %v", ErrPreflightFailed, err)
		}
		return nil
	case <-time.After(timeout):
		return ErrPreflightTimeout
	case <-ctx.Done():
		return ctx.Err()
	}
}

// launchStorageProbe transfers adapter ownership to the asynchronous probe.
// Timeout limits the caller's wait, and shutdown still joins the probe.
func launchStorageProbe(adapter storage.Adapter, launch func(func())) <-chan error {
	result := make(chan error, 1)
	launch(func() {
		defer storage.CloseAdapter(adapter)
		result <- adapter.TestConnection()
	})
	return result
}

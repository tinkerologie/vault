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
	return preflightAdapter(ctx, adapter, preflightTimeout, launch)
}

func preflightAdapter(ctx context.Context, adapter storage.Adapter, timeout time.Duration, launch func(func())) error {
	err, returned := probeConnection(ctx, adapter, timeout, launch)
	if !returned {
		return err
	}
	storage.CloseAdapter(adapter)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPreflightFailed, err)
	}
	return nil
}

// probeConnection owns the adapter until the caller receives the result.
// If the wait expires first, the probe closes the adapter when it finishes.
func probeConnection(ctx context.Context, adapter storage.Adapter, timeout time.Duration, launch func(func())) (err error, returned bool) {
	result := make(chan error)
	abandoned := make(chan struct{})
	defer close(abandoned)
	launch(func() {
		err := adapter.TestConnection()
		select {
		case result <- err:
		case <-abandoned:
			storage.CloseAdapter(adapter)
		}
	})
	select {
	case err := <-result:
		return err, true
	case <-time.After(timeout):
		return ErrPreflightTimeout, false
	case <-ctx.Done():
		return ctx.Err(), false
	}
}

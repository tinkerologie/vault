package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/storage"
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
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return fmt.Errorf("%w: adapter construction: %v", ErrPreflightFailed, err)
	}
	defer storage.CloseAdapter(adapter)
	return preflightAdapter(ctx, adapter, preflightTimeout)
}

// preflightAdapter is the testable core — it operates on a pre-built
// adapter so unit tests can inject stubs without going through the
// storage factory.
func preflightAdapter(_ context.Context, adapter storage.Adapter, timeout time.Duration) error {
	resultCh := make(chan error, 1)
	go func() { resultCh <- adapter.TestConnection() }()
	select {
	case err := <-resultCh:
		if err != nil {
			return fmt.Errorf("%w: %v", ErrPreflightFailed, err)
		}
		return nil
	case <-time.After(timeout):
		return ErrPreflightTimeout
	}
}

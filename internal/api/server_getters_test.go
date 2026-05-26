package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// TestServer_Getters exercises every accessor / setter on *Server that
// is currently uncovered:
//
//   - Hub() / Runner() / Syncer() / SettingsHandler() / BrowseHandler()
//   - SetReplicationSyncer / SetScheduleReloader / SetNextRunResolver /
//     SetConfigChangeHook
//
// These are all trivial pass-through wrappers; the test simply confirms
// the returned value matches what was supplied (or that the setter is
// safe to call on a server that has not been fully wired).
func TestServer_Getters(t *testing.T) {
	t.Parallel()
	d := testDB(t)
	srv := NewServer(d, ServerConfig{Addr: ":0"})

	if srv.Hub() == nil {
		t.Error("Hub() returned nil")
	}
	// Runner is initialised by NewServer.
	if srv.Runner() == nil {
		t.Error("Runner() returned nil")
	}

	// SetReplicationSyncer is a pass-through; storing nil is fine because
	// the test never invokes the syncer.
	srv.SetReplicationSyncer(nil)
	if srv.Syncer() != nil {
		t.Error("Syncer() should be nil after SetReplicationSyncer(nil)")
	}

	// SettingsHandler / BrowseHandler are wired by NewServer.
	if srv.SettingsHandler() == nil {
		t.Error("SettingsHandler() returned nil")
	}
	if srv.BrowseHandler() == nil {
		t.Error("BrowseHandler() returned nil")
	}

	// SetScheduleReloader: pass-through, accept nil-safe wiring.
	srv.SetScheduleReloader(func() error { return nil })

	// SetNextRunResolver: pass-through wiring; never invoked by this test.
	srv.SetNextRunResolver(func(int64) (string, bool) { return "", false })

	// SetConfigChangeHook: cascade-set on every sub-handler. Calling it
	// with a non-nil hook also verifies the cascading nil-guards work.
	srv.SetConfigChangeHook(func() {})
}

// TestServer_StartWithContext_ShutsDownCleanly drives StartWithContext on
// an ephemeral port and immediately cancels the context. The function
// kicks off the listen+serve loop AND a parallel goroutine that calls
// runner.Drain + srv.Shutdown on ctx.Done. We just need the listen loop
// to return so we observe the post-Shutdown error (http.ErrServerClosed).
func TestServer_StartWithContext_ShutsDownCleanly(t *testing.T) {
	// Cannot run in parallel: it binds a real socket, even on :0.
	d := testDB(t)
	srv := NewServer(d, ServerConfig{Addr: "127.0.0.1:0"})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so the shutdown goroutine triggers Shutdown
	// before / during the listen loop.
	cancel()

	// StartWithContext blocks until the inner srv.Shutdown completes; with
	// the ctx already cancelled the goroutine inside StartWithContext
	// fires Drain+Shutdown almost immediately. Bound the test to a few
	// seconds in case the implementation ever changes.
	errCh := make(chan error, 1)
	go func() { errCh <- srv.StartWithContext(ctx) }()

	select {
	case err := <-errCh:
		// http.ErrServerClosed is the expected, healthy return when
		// Shutdown is called. Any other error is a real failure.
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("StartWithContext returned unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StartWithContext did not return within 10s after ctx cancel")
	}
}

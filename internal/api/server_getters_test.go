package api

import (
	"testing"
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

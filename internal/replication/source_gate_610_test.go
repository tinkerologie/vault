package replication

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
)

func gateCount(s *Syncer) int {
	s.gatesMu.Lock()
	defer s.gatesMu.Unlock()
	return len(s.gates)
}

// Requests for sources that don't exist leave no lock entries behind, so a
// caller can't grow the map by naming made-up IDs.
func TestSourceGatesMissingSources610(t *testing.T) {
	t.Parallel()
	s := NewSyncer(openTestDB(t), nil)
	for id := int64(1000); id < 1100; id++ {
		called := false
		if err := s.UpdateSource(id, func(bool) error { called = true; return nil }); !errors.Is(err, db.ErrNotFound) || called {
			t.Fatalf("UpdateSource(%d) = %v (update called %v), want db.ErrNotFound", id, err, called)
		}
		if _, err := s.SyncSource(id, nil); !errors.Is(err, db.ErrNotFound) || !strings.Contains(err.Error(), fmt.Sprintf("source %d:", id)) {
			t.Fatalf("SyncSource(%d) = %v, want db.ErrNotFound naming the source", id, err)
		}
	}
	if n := gateCount(s); n != 0 {
		t.Errorf("lock entries = %d after requests for missing sources, want 0", n)
	}
}

// Entries are removed once nothing for the source is running, however syncs
// and updates of it interleave.
func TestSourceGatesReleasedWhenIdle610(t *testing.T) {
	t.Parallel()
	var downloads atomic.Int64
	srv := peer585(t, []RemoteRestorePoint{{ID: 1, BackupType: "full", StoragePath: "J/run1"}}, &downloads)
	d, srcID := setup585(t, srv.URL)
	other, err := d.CreateReplicationSource(db.ReplicationSource{Name: "Q", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSyncer(d, nil)

	var wg sync.WaitGroup
	var mu sync.Mutex
	updating := map[int64]bool{}
	for i := range 40 {
		id := []int64{srcID, other}[i%2]
		wg.Add(2)
		go func() {
			defer wg.Done()
			end := s.beginSync(id)
			end()
		}()
		go func() {
			defer wg.Done()
			if err := s.UpdateSource(id, func(bool) error {
				mu.Lock()
				if updating[id] {
					t.Errorf("two updates of source %d at once", id)
				}
				updating[id] = true
				mu.Unlock()
				runtime.Gosched()
				mu.Lock()
				updating[id] = false
				mu.Unlock()
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := s.SyncSource(srcID, nil); err != nil {
		t.Fatal(err)
	}
	if n := gateCount(s); n != 0 {
		t.Errorf("lock entries = %d with nothing running, want 0", n)
	}

	// While a sync runs its entry stays, and an update sees it.
	end := s.beginSync(srcID)
	if err := s.UpdateSource(srcID, func(syncing bool) error {
		if !syncing {
			t.Error("update did not see the running sync")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := gateCount(s); n != 1 {
		t.Errorf("lock entries = %d during a sync, want 1", n)
	}
	end()
	if n := gateCount(s); n != 0 {
		t.Errorf("lock entries = %d after the sync, want 0", n)
	}
}

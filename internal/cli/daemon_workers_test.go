package cli

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/tinkerologie/vault/internal/anomaly"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/scheduler"
)

func TestDaemonWorkersJoinBlockedLoop(t *testing.T) {
	d := openTestDB(t)
	path := filepath.Join(t.TempDir(), "final.db")
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		waiting, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		workers := daemonWorkers{
			scheduler: scheduler.New(d, func(int64) {}),
			snapshots: db.NewSnapshotManager(d, path, path),
			evaluator: anomaly.NewEvaluator(d, nil, &anomaly.Registry{}, anomaly.RealClock{}),
			throttle: func(ctx context.Context) {
				<-ctx.Done()
				close(waiting)
				<-release
			},
		}
		shutdown := workers.start(ctx)
		cancel()
		<-waiting
		shutdown.WaitForDispatch()
		joined := make(chan struct{})
		go func() {
			shutdown.Join()
			close(joined)
		}()
		synctest.Wait()
		select {
		case <-joined:
			t.Fatal("worker cleanup finished before the in-flight work completed")
		default:
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("snapshot appeared before the worker finished: %v", err)
		}
		unblock()
		<-joined
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("final snapshot missing after join: %v", err)
		}
	})
}

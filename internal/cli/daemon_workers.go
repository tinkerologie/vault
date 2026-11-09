package cli

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/tinkerologie/vault/internal/anomaly"
	"github.com/tinkerologie/vault/internal/api"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/discovery"
	"github.com/tinkerologie/vault/internal/scheduler"
)

// daemonWorkers owns work shared by the daemon and replica. The API runtime
// cancels its context when serving stops and calls the returned join before
// the CLI closes the database.
type daemonWorkers struct {
	scheduler     *scheduler.Scheduler
	throttle      func(context.Context)
	snapshots     *db.SnapshotManager
	evaluator     *anomaly.Evaluator
	heartbeatPath string
	discovery     bool
	addr          string
	tls           bool
}

func (w daemonWorkers) run(ctx context.Context, srv *api.Server) error {
	// Preserve the immediate first write even if the listener or TLS
	// credentials fail. The CLI owns and joins this startup worker.
	if w.heartbeatPath != "" {
		stopHeartbeat := daemonStartHeartbeat(ctx, w.heartbeatPath, version)
		defer stopHeartbeat()
	}
	return srv.Run(ctx, w.start)
}

func (w daemonWorkers) start(ctx context.Context) api.WorkerShutdown {
	var loops sync.WaitGroup
	if w.evaluator != nil {
		w.evaluator.Start()
		loops.Go(func() { runAnomalyTrendTicker(ctx, w.evaluator) })
	}
	schedulerDone := make(chan struct{})
	if err := w.scheduler.Start(); err != nil {
		log.Printf("Warning: scheduler failed to start: %v", err)
		close(schedulerDone)
	} else {
		go func() {
			defer close(schedulerDone)
			<-ctx.Done()
			w.scheduler.Stop()
		}()
	}

	if w.throttle != nil {
		loops.Go(func() { w.throttle(ctx) })
	}
	if w.snapshots != nil {
		loops.Go(func() { runPeriodicSnapshots(ctx, w.snapshots) })
	}
	joinDiscovery := startDiscovery(ctx, w.discovery, w.addr, w.tls)
	return api.WorkerShutdown{
		WaitForDispatch: func() { <-schedulerDone },
		Join: func() {
			loops.Wait()
			joinDiscovery()
			if w.evaluator != nil {
				_ = w.evaluator.Drain(context.WithoutCancel(ctx))
			}
			if w.snapshots != nil {
				if err := w.snapshots.Close(); err != nil {
					log.Printf("Warning: final snapshot save failed: %v", err)
				}
			}
		},
	}
}

// runPeriodicSnapshots refreshes the pool snapshot. The snapshot manager
// throttles the USB shadow copy to at most one write per hour.
func runPeriodicSnapshots(ctx context.Context, snapshots *db.SnapshotManager) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := snapshots.SaveSnapshotAndUSBBackup(); err != nil {
				log.Printf("Warning: periodic snapshot save failed: %v", err)
			}
		}
	}
}

// startDiscovery advertises non-loopback listeners unless mDNS was disabled.
func startDiscovery(ctx context.Context, enabled bool, addr string, tlsEnabled bool) func() {
	if !enabled {
		log.Println("discovery: mDNS advertisement disabled by configuration (--mdns=false)")
		return func() {}
	}
	return discovery.New(addr, version, tlsEnabled).Start(ctx)
}

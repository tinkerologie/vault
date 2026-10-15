package engine

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func statsBody458(t *testing.T, s containertypes.StatsResponse) client.ContainerStatsResult {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return client.ContainerStatsResult{Body: io.NopCloser(strings.NewReader(string(b)))}
}

func statsSample458(read time.Time, cpu, sys uint64, online uint32, percpu []uint64, rx, tx uint64) containertypes.StatsResponse {
	return containertypes.StatsResponse{
		Read: read,
		CPUStats: containertypes.CPUStats{
			CPUUsage:    containertypes.CPUUsage{TotalUsage: cpu, PercpuUsage: percpu},
			SystemUsage: sys,
			OnlineCPUs:  online,
		},
		Networks: map[string]containertypes.NetworkStats{"eth0": {RxBytes: rx, TxBytes: tx}},
	}
}

// ProbeActivity takes two samples one second apart, so the cases that reach
// the second sample each take ~1s; they run in parallel.
func TestProbeActivity458(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		samples []containertypes.StatsResponse // served in order
		errAt   int                            // 1-based call that fails; 0 = none
		garbage bool                           // first body is not JSON
		cancel  bool                           // cancel during the wait
		want    ActivitySample
	}{
		{
			name: "cpu and network rates",
			samples: []containertypes.StatsResponse{
				statsSample458(t0, 1000, 10000, 2, nil, 100, 100),
				statsSample458(t0.Add(2*time.Second), 2000, 20000, 2, nil, 300, 500),
			},
			want: ActivitySample{Known: true, CPUPercent: 20, NetBytesPerSec: 300},
		},
		{
			name: "online cpus from per-cpu list, counters reset",
			samples: []containertypes.StatsResponse{
				statsSample458(t0, 1000, 10000, 0, []uint64{1, 1}, 900, 0),
				statsSample458(t0, 1500, 15000, 0, []uint64{1, 1, 1, 1}, 10, 0),
			},
			want: ActivitySample{Known: true, CPUPercent: 40},
		},
		{name: "first sample fails", errAt: 1},
		{name: "first sample undecodable", garbage: true},
		{name: "second sample fails", samples: []containertypes.StatsResponse{statsSample458(t0, 1, 1, 1, nil, 0, 0)}, errAt: 2},
		{name: "cancelled between samples", samples: []containertypes.StatsResponse{statsSample458(t0, 1, 1, 1, nil, 0, 0)}, cancel: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mu sync.Mutex
			call := 0
			f := &fakeDocker458{containerStats: func(context.Context, string) (client.ContainerStatsResult, error) {
				mu.Lock()
				defer mu.Unlock()
				call++
				if call == tt.errAt {
					return client.ContainerStatsResult{}, errFake458
				}
				if tt.garbage {
					return client.ContainerStatsResult{Body: io.NopCloser(strings.NewReader("<html>"))}, nil
				}
				if tt.cancel {
					cancel()
				}
				return statsBody458(t, tt.samples[call-1]), nil
			}}
			got := (&ContainerHandler{cli: f}).ProbeActivity(ctx, "cid")
			if got.Known != tt.want.Known || math.Abs(got.CPUPercent-tt.want.CPUPercent) > 1e-9 || math.Abs(got.NetBytesPerSec-tt.want.NetBytesPerSec) > 1e-9 {
				t.Errorf("ProbeActivity = %+v, want %+v", got, tt.want)
			}
		})
	}
}

package runner

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ruaan-deysel/vault/internal/docsmeta"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// RunAutoThrottleLoop is the adaptive upload-throttle controller (issue
// #237): every tick it estimates EXTERNAL upstream traffic on the busiest
// NIC (total tx minus Vault's own uploads) and retunes the process-wide
// upload limit to link_capacity − external − 10% headroom, clamped between
// the configured floor and the link capacity. Settings are re-read each tick
// so enabling/disabling or retuning needs no daemon restart. On hosts
// without /proc/net/dev (non-Linux dev builds) the loop idles harmlessly.
func (r *Runner) RunAutoThrottleLoop(ctx context.Context) {
	const tick = 5 * time.Second
	var prevTx, prevVault int64
	var prevIface string
	var smoothedExt float64
	havePrev := false
	wasEnabled := false

	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			storage.SetAutoThrottleLimit(0)
			return
		case <-t.C:
		}

		enabled, _ := r.db.GetSetting("auto_throttle_enabled", docsmeta.DefaultFor("auto_throttle_enabled"))
		linkStr, _ := r.db.GetSetting("auto_throttle_link_mbps", docsmeta.DefaultFor("auto_throttle_link_mbps"))
		floorStr, _ := r.db.GetSetting("auto_throttle_floor_mbps", docsmeta.DefaultFor("auto_throttle_floor_mbps"))
		linkMbps, _ := strconv.Atoi(linkStr)
		floorMbps, _ := strconv.Atoi(floorStr)

		if enabled != "true" || linkMbps <= 0 {
			if wasEnabled {
				log.Printf("runner: adaptive upload throttle disabled")
			}
			storage.SetAutoThrottleLimit(0)
			havePrev, wasEnabled = false, false
			continue
		}
		if !wasEnabled {
			log.Printf("runner: adaptive upload throttle enabled (link %d Mbps, floor %d Mbps)", linkMbps, floorMbps)
			wasEnabled = true
		}

		iface, totalTx, err := busiestInterfaceTxBytes()
		if err != nil {
			storage.SetAutoThrottleLimit(0)
			havePrev = false
			continue
		}
		vaultTx := storage.AutoThrottleUploadedBytes()
		// The baseline is only valid against the SAME interface's counter,
		// moving forward — a winner change or a counter reset would produce
		// an arbitrary delta that whipsaws the limiter.
		if iface != prevIface || totalTx < prevTx {
			havePrev = false
		}
		if havePrev {
			externalBps := (float64(totalTx-prevTx) - float64(vaultTx-prevVault)) / tick.Seconds()
			if externalBps < 0 {
				externalBps = 0
			}
			// EMA smoothing damps the phase error between Vault's
			// source-reader byte counting and actual NIC transmission
			// (buffered uploads), preventing floor/capacity oscillation.
			smoothedExt = 0.5*smoothedExt + 0.5*externalBps
			storage.SetAutoThrottleLimit(autoThrottleTarget(linkMbps, floorMbps, smoothedExt))
		} else {
			smoothedExt = 0
		}
		prevTx, prevVault, prevIface, havePrev = totalTx, vaultTx, iface, true
	}
}

// autoThrottleTarget computes the new Vault upload budget in bytes/sec:
// capacity − external − 10% headroom, clamped to [floor, capacity]. The
// floor is itself clamped to [1 Mbps, capacity]: a zero budget would be
// indistinguishable from "throttle disabled" at the limiter (fail-open), so
// the controller always leaves Vault a trickle instead of pausing.
func autoThrottleTarget(linkMbps, floorMbps int, externalBytesPerSec float64) float64 {
	const mbps = 1_000_000.0 / 8
	capBps := float64(linkMbps) * mbps
	floorBps := float64(floorMbps) * mbps
	if floorBps < 1*mbps {
		floorBps = 1 * mbps
	}
	if floorBps > capBps {
		floorBps = capBps
	}
	target := capBps - externalBytesPerSec - capBps*0.10
	if target < floorBps {
		target = floorBps
	}
	if target > capBps {
		target = capBps
	}
	return target
}

// busiestInterfaceTxBytes returns the cumulative transmitted bytes of the
// physical interface with the highest tx counter — on an Unraid host that is
// the uplink NIC or its bridge. Loopback and virtual container/VM interfaces
// are skipped so container-to-container chatter doesn't count as uplink use.
func busiestInterfaceTxBytes() (string, int64, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	var bestName string
	var best int64 = -1
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "virbr") || strings.HasPrefix(name, "vnet") || strings.HasPrefix(name, "tun") ||
			strings.HasPrefix(name, "wg") {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) < 9 {
			continue
		}
		tx, err := strconv.ParseInt(fields[8], 10, 64)
		if err != nil {
			continue
		}
		if tx > best {
			best, bestName = tx, name
		}
	}
	if best < 0 {
		return "", 0, fmt.Errorf("no usable interface in /proc/net/dev")
	}
	return bestName, best, sc.Err()
}

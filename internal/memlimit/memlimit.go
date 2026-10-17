// Package memlimit applies the daemon's soft memory limit (issue #500).
//
// The limit comes from the memory_limit_mb setting and is applied with
// debug.SetMemoryLimit, so it can change at runtime without restarting the
// daemon. A GOMEMLIMIT set in the process environment always wins: the Go
// runtime has already applied it at start-up, and the setting is then only
// recorded, never applied.
//
// The limit is soft. When the live heap exceeds it the garbage collector
// runs almost continuously, so a limit below the legitimate working set
// (for example S3 multipart buffers) costs CPU instead of saving memory.
// That is why the default is off.
package memlimit

import (
	"fmt"
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
)

// Limit sources reported by Current.
const (
	SourceOff         = "off"
	SourceSetting     = "setting"
	SourceEnvironment = "environment"
)

// Accepted memory_limit_mb values: 0 (off) or MinMB..MaxMB.
const (
	MinMB = 512
	MaxMB = 1 << 20 // 1 TiB
)

// envLimitSet reports whether GOMEMLIMIT was set when the process started.
// A package variable so tests can simulate the environment override.
var envLimitSet = strings.TrimSpace(os.Getenv("GOMEMLIMIT")) != ""

// setMemoryLimit is debug.SetMemoryLimit, swappable so tests never leave a
// real limit behind.
var setMemoryLimit = debug.SetMemoryLimit

var (
	mu        sync.Mutex
	settingMB int64
)

// ParseMB validates a memory_limit_mb setting value. An empty value means
// off.
func ParseMB(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || (n != 0 && (n < MinMB || n > MaxMB)) {
		return 0, fmt.Errorf("invalid memory_limit_mb %q: use 0 (off) or a whole number of MiB from %d to %d", v, MinMB, MaxMB)
	}
	return n, nil
}

// Apply records mb as the configured limit and applies it, unless
// GOMEMLIMIT overrides it. 0 removes the limit.
func Apply(mb int64) {
	mu.Lock()
	defer mu.Unlock()
	settingMB = mb
	if envLimitSet {
		return
	}
	if mb <= 0 {
		setMemoryLimit(math.MaxInt64)
		return
	}
	setMemoryLimit(mb << 20)
}

// State describes the effective memory limit.
type State struct {
	// LimitBytes is the effective soft limit; 0 means unlimited.
	LimitBytes int64 `json:"limit_bytes"`
	// Source is SourceOff, SourceSetting or SourceEnvironment.
	Source string `json:"source"`
	// SettingMB is the stored memory_limit_mb value (0 = off). Under an
	// environment override it is recorded but not in effect.
	SettingMB int64 `json:"setting_mb"`
	// EnvironmentOverride reports that GOMEMLIMIT is set and wins.
	EnvironmentOverride bool `json:"environment_override"`
}

// Current returns the effective limit and where it came from.
func Current() State {
	mu.Lock()
	defer mu.Unlock()
	limit := setMemoryLimit(-1) // a negative value reads without changing it
	st := State{SettingMB: settingMB, EnvironmentOverride: envLimitSet}
	if limit > 0 && limit < math.MaxInt64 {
		st.LimitBytes = limit
	}
	switch {
	case envLimitSet:
		st.Source = SourceEnvironment
	case st.LimitBytes > 0:
		st.Source = SourceSetting
	default:
		st.Source = SourceOff
	}
	return st
}

// SuggestedMB proposes a limit for a host with hostTotalBytes of RAM: about
// 12% of it, rounded down to 64 MiB, and never below MinMB. With an unknown
// host size it returns MinMB.
func SuggestedMB(hostTotalBytes uint64) int64 {
	mb := int64(hostTotalBytes/(1<<20)) * 12 / 100 //nolint:gosec // G115: host RAM in MiB fits int64
	mb -= mb % 64
	return max(mb, MinMB)
}

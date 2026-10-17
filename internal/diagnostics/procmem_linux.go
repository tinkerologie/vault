//go:build linux

package diagnostics

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// procStatusPath and procMeminfoPath are variables so tests can point them
// at fixtures.
var (
	procStatusPath  = "/proc/self/status"
	procMeminfoPath = "/proc/meminfo"
	procStatPath    = "/proc/self/stat"
)

// clockTicks is USER_HZ, the unit of /proc/self/stat CPU times. It is 100 on
// every Linux architecture Vault ships for; reading it properly needs
// sysconf, which a CGO-free build cannot call.
const clockTicks = 100

// readProcCPU returns the CPU time the process has used (utime + stime from
// /proc/self/stat) in seconds, or 0 if it is unreadable.
func readProcCPU() float64 {
	b, err := os.ReadFile(procStatPath) // #nosec G304 -- fixed /proc path (overridable only in tests)
	if err != nil {
		return 0
	}
	// The command name in field 2 may contain spaces, so count fields from
	// the closing parenthesis: utime and stime are fields 14 and 15.
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 13 {
		return 0
	}
	ut, err1 := strconv.ParseUint(f[11], 10, 64)
	st, err2 := strconv.ParseUint(f[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0
	}
	return float64(ut+st) / clockTicks
}

// readProcRSS returns the process's current (VmRSS) and peak (VmHWM)
// resident set size in bytes, or zeros if /proc is unreadable.
func readProcRSS() (rss, peak uint64) {
	fields := readKBFields(procStatusPath, "VmRSS", "VmHWM")
	return fields["VmRSS"], fields["VmHWM"]
}

// readHostMemTotal returns MemTotal from /proc/meminfo in bytes, or 0.
func readHostMemTotal() uint64 {
	return readKBFields(procMeminfoPath, "MemTotal")["MemTotal"]
}

// readKBFields parses "Key:   1234 kB" lines from a /proc file and returns
// the requested keys in bytes. Missing or malformed keys are omitted.
func readKBFields(path string, keys ...string) map[string]uint64 {
	out := make(map[string]uint64, len(keys))
	f, err := os.Open(path) // #nosec G304 -- fixed /proc paths (overridable only in tests)
	if err != nil {
		return out
	}
	defer f.Close()
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || !want[key] {
			continue
		}
		f := strings.Fields(rest)
		if len(f) != 2 || f[1] != "kB" {
			continue
		}
		if kb, err := strconv.ParseUint(f[0], 10, 64); err == nil {
			out[key] = kb * 1024
		}
	}
	return out
}

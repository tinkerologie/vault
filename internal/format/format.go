// Package format holds the project's single rendering contract for values
// that reach an operator — byte counts, durations, and transfer rates.
//
// It exists because the same number was being rendered three different ways:
// notifications divided by 1024 twice and printed "%.1f MB" (so a 2 TB backup
// read as "2097152.0 MB"), while internal/engine and internal/anomaly each
// carried their own copy of an adaptive formatter. Anything that shows a size
// to a user should call Bytes, so the number a person sees is the same
// wherever they see it.
package format

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// MinThroughputDuration is the shortest operation a transfer rate is shown
// for. Below it, timer resolution and fixed overheads dominate and a rate is
// meaningless (a tiny restore finishing in a few milliseconds would report
// thousands of GB/s, #455). The web UI's formatSpeed applies the same limit.
const MinThroughputDuration = time.Second

// Duration renders an elapsed time for run logs without hiding sub-second
// work: "<1s" below one second, one decimal place below a minute ("2.4s"),
// and whole seconds above that ("1m5s", Go duration style). Values round to
// the nearest step, like the web UI's formatDuration; the web UI shows whole
// seconds and a space ("1m 5s") because it has less room.
func Duration(d time.Duration) string {
	switch {
	case d < 0:
		return "—"
	case d < time.Second:
		return "<1s"
	}
	if tenths := math.Round(d.Seconds() * 10); tenths < 600 {
		return strconv.FormatFloat(tenths/10, 'f', 1, 64) + "s"
	}
	return d.Round(time.Second).String()
}

// Throughput renders bytes moved over d as a rate such as "31.2 MB/s",
// dividing by fractional seconds. ok is false — and no rate should be shown —
// when nothing moved or d is shorter than MinThroughputDuration.
func Throughput(bytes int64, d time.Duration) (rate string, ok bool) {
	if bytes <= 0 || d < MinThroughputDuration {
		return "", false
	}
	return Bytes(float64(bytes)/d.Seconds()) + "/s", true
}

// Bytes renders a byte count as an adaptive, human-friendly string
// (B / KB / MB / GB / TB / PB), matching the web UI's formatBytes helper:
// 1024-based units, one decimal place, with a trailing ".0" trimmed — so
// 34359738368 → "32 GB", 1572864 → "1.5 MB", 1536 → "1.5 KB".
//
// The unit is chosen by dividing until the value drops below 1024, so a count
// just under a boundary renders in the smaller unit rather than as "1024 KB".
func Bytes(b float64) string {
	if math.IsNaN(b) || math.IsInf(b, 0) {
		return "—"
	}
	// -0 compares equal to 0 but formats as "-0", so short-circuit it here
	// rather than letting it reach the sign branch or the formatter.
	if b == 0 {
		return "0 B"
	}
	if b < 0 {
		return "-" + Bytes(-b)
	}
	const k = 1024.0
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	v := b
	for v >= k && i < len(units)-1 {
		v /= k
		i++
	}
	// A value that rounds up to 1024 at its unit belongs in the next one.
	// Without this, one byte under a petabyte renders as "1024 TB" here while
	// the web UI's log-based index picks "1 PB" — the two disagree at exactly
	// the boundaries, which is where a mismatch is most visible.
	if i < len(units)-1 && math.Round(v*10)/10 >= k {
		v /= k
		i++
	}
	if i == 0 {
		// Whole bytes — no fractional part. Round explicitly for the same
		// reason as below: %.0f breaks a tie to even (0.5 -> "0") while
		// JavaScript's Math.round takes it up. Fractional byte counts reach
		// here from the Discord transfer rate, which is a division.
		return fmt.Sprintf("%.0f %s", math.Round(v), units[i])
	}
	// Round explicitly rather than leaving it to the formatter: Go's
	// FormatFloat breaks a tie to even (1.25 -> "1.2") while JavaScript's
	// toFixed rounds it up ("1.3"), so the same byte count read differently
	// in the daemon and the interface. Both now round half away from zero.
	s := strconv.FormatFloat(math.Round(v*10)/10, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + units[i]
}

package engine

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// humanizeBytes renders a byte count as an adaptive, human-friendly string
// (B / KB / MB / GB / TB / PB), matching the web UI's formatBytes helper and
// the identical helper in internal/anomaly: 1024-based units, one decimal
// place, with a trailing ".0" trimmed (so 34359738368 → "32 GB",
// 6453198848 → "6 GB"). Used in VM backup/restore progress messages so
// operators see "6 GB/32 GB" instead of "6453198848/34359738368 bytes"
// (issue #133).
func humanizeBytes(b float64) string {
	if math.IsNaN(b) || math.IsInf(b, 0) {
		return "—"
	}
	if b < 0 {
		return "-" + humanizeBytes(-b)
	}
	const k = 1024.0
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	v := b
	for v >= k && i < len(units)-1 {
		v /= k
		i++
	}
	if i == 0 {
		// Whole bytes — no fractional part.
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + units[i]
}

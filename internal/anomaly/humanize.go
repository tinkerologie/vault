package anomaly

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// humanizeBytes renders a byte count as an adaptive, human-friendly string
// (B / KB / MB / GB / TB / PB), matching the web UI's formatBytes helper:
// 1024-based units, one decimal place, with a trailing ".0" trimmed (so
// 4_259_532_913 → "4 GB", 1_572_864 → "1.5 MB"). Used in anomaly summary
// strings so operators see "4 GB" instead of "4259532913 bytes".
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

// roundTo rounds v to the given number of decimal places. Non-finite values
// (NaN/Inf) pass through unchanged — callers guard against them separately and
// json.Marshal would reject them either way. Used to keep Details JSON values
// human-friendly (z_score -16.76 instead of -16.76413455138884, issue #134).
func roundTo(v float64, decimals int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	multiplier := math.Pow(10, float64(decimals))
	return math.Round(v*multiplier) / multiplier
}

// humanizeDuration renders a duration in seconds as an adaptive, human-friendly
// string, matching the web UI's formatDuration helper: "45s", "5m 26s",
// "2h 13m". Used in anomaly summary strings so operators see "5m 26s" instead
// of "326s".
func humanizeDuration(seconds float64) string {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return "—"
	}
	s := int64(math.Round(seconds))
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %dm", s/3600, (s%3600)/60)
}

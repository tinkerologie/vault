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

// humanizeMultiplier renders a growth factor as a friendly "N×" string for use
// in summary sentences (e.g. 1.18 → "1.2×", 5.0 → "5×"). Whole multiples drop
// the decimal so "5×" reads cleanly; fractional ones keep one decimal.
//
// A tight MAD can fire a z-score anomaly at a growth factor only just above 1,
// where rounding to a bare "1×" would read like no change at all. Such values
// collapse to ">1×" (and "<1×" below 1) so the summary still signals a real
// deviation.
func humanizeMultiplier(factor float64) string {
	if math.IsNaN(factor) || math.IsInf(factor, 0) {
		return "—"
	}
	r := roundTo(factor, 1)
	if r == 1 && factor > 1 {
		return ">1×"
	}
	if r == 1 && factor < 1 {
		return "<1×"
	}
	if r == math.Trunc(r) {
		return fmt.Sprintf("%.0f×", r)
	}
	return fmt.Sprintf("%.1f×", r)
}

// humanizePercent renders a fraction (0–1) as a whole-percent string
// (e.g. 0.45 → "45%"). Used when a summary compares an observed value against
// the usual one as a share rather than a multiple.
func humanizePercent(fraction float64) string {
	if math.IsNaN(fraction) || math.IsInf(fraction, 0) {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", fraction*100)
}

// humanizeDays renders a day count as a friendly phrase for runway/ETA text
// (e.g. 0.4 → "less than a day", 1 → "1 day", 5.6 → "6 days").
func humanizeDays(days float64) string {
	if math.IsNaN(days) || math.IsInf(days, 0) || days < 0 {
		return "—"
	}
	r := math.Round(days)
	switch {
	case r < 1:
		return "less than a day"
	case r == 1:
		return "1 day"
	default:
		return fmt.Sprintf("%.0f days", r)
	}
}

package anomaly

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// commaGroup renders an integer with thousands separators when the magnitude
// is large enough to warrant them (|n| >= 1000), e.g. 233 → "233",
// 4000 → "4,000", 1234567 → "1,234,567". Smaller values render plainly. It
// backs the whole-percent strings so a large change reads "99,900%" rather
// than the harder-to-scan "99900%".
func commaGroup(n int64) string {
	neg := n < 0
	// Negate into an unsigned magnitude rather than -n: for math.MinInt64 the
	// negation overflows back to a negative value and would emit a double minus.
	var u uint64
	if neg {
		u = uint64(-(n + 1)) + 1
	} else {
		u = uint64(n)
	}
	s := strconv.FormatUint(u, 10)
	if u < 1000 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	if pre := len(s) % 3; pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := len(s) % 3; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
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

// humanizePercent renders a fraction (0–1) as a whole-percent string
// (e.g. 0.45 → "45%"). Used when a summary compares an observed value against
// the usual one as a share rather than a multiple. A non-zero fraction that
// rounds below 1% renders as "<1%" rather than "0%" — a critical low-free
// alert must not claim zero space remains when some is still present.
func humanizePercent(fraction float64) string {
	if math.IsNaN(fraction) || math.IsInf(fraction, 0) {
		return "—"
	}
	pct := math.Round(fraction * 100)
	if pct < 1 && fraction > 0 {
		return "<1%"
	}
	return commaGroup(int64(pct)) + "%"
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

// humanizePercentChange renders a growth factor as a concise change phrase
// ("increased by 30%", "decreased by 65%") for the post-#315 concise summary
// style. Growth is (factor-1)×100, shrinkage is (1-factor)×100 — the standard
// reading of "increased/decreased by". Changes that would round below 1%
// clamp to 1% so a fired anomaly never reads as "0%"; the exact factor and
// raw values stay in Details for investigation.
// clampInt64 coerces v into the int64 range by saturating, instead of letting an
// out-of-range float→int64 conversion wrap to an implementation-defined value.
// +Inf and NaN (which arise when the percent arithmetic overflows for very
// large factors) saturate to MaxInt64 so a huge factor can never render as a
// bogus negative percentage.
func clampInt64(v float64) int64 {
	if math.IsNaN(v) || v >= math.MaxInt64 {
		return math.MaxInt64
	}
	if v <= math.MinInt64 {
		return math.MinInt64
	}
	return int64(v)
}

func humanizePercentChange(factor float64) string {
	if math.IsNaN(factor) || math.IsInf(factor, 0) {
		return "—"
	}
	if factor >= 1 {
		pct := math.Max((factor-1)*100, 1)
		return fmt.Sprintf("increased by %s%%", commaGroup(clampInt64(math.Round(pct))))
	}
	pct := math.Max((1-factor)*100, 1)
	return fmt.Sprintf("decreased by %s%%", commaGroup(clampInt64(math.Round(pct))))
}

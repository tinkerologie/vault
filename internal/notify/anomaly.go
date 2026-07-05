package notify

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Anomaly Discord embed colour constants (hex values stored as int).
// Used by BuildAnomalyEmbed so callers don't hard-code magic numbers.
const (
	ColorAnomalyCritical = 0xDC2626 // red
	ColorAnomalyWarning  = 0xF59E0B // amber
	ColorAnomalyResolved = 0x10B981 // green
)

// AnomalyEmbedParams carries the fields needed to build an anomaly Discord embed.
type AnomalyEmbedParams struct {
	// Severity is one of "critical", "warning", "info" or "resolved".
	Severity string
	// ScopeKind is "job" or "destination".
	ScopeKind string
	// ScopeName is a human-readable name for the scope (job name, dest name, etc.).
	ScopeName string
	// Summary is the one-line anomaly description.
	Summary string
	// Details is additional structured context (may be empty).
	Details string
	// IsUpdate is true when this is an escalation (anomaly was already open).
	IsUpdate bool
}

// BuildAnomalyEmbed constructs a DiscordEmbed for an anomaly notification.
// The embed colour is determined by severity; an "info" severity maps to
// ColorInfo (blurple) to avoid overloading warning-level colours.
func BuildAnomalyEmbed(p AnomalyEmbedParams) DiscordEmbed {
	var color int
	switch p.Severity {
	case "critical":
		color = ColorAnomalyCritical
	case "warning":
		color = ColorAnomalyWarning
	case "resolved":
		color = ColorAnomalyResolved
	case "info":
		color = ColorInfo
	default:
		color = ColorInfo
	}

	action := "Raised"
	if p.IsUpdate {
		action = "Escalated"
	}

	title := fmt.Sprintf("Anomaly %s: %s", action, p.Summary)

	var fields []DiscordField
	if p.ScopeName != "" {
		fields = append(fields, DiscordField{
			Name:   scopeLabel(p.ScopeKind),
			Value:  p.ScopeName,
			Inline: true,
		})
	}
	fields = append(fields, DiscordField{
		Name:   "Severity",
		Value:  p.Severity,
		Inline: true,
	})
	if ctx := renderAnomalyDetails(p.Details); ctx != "" {
		fields = append(fields, DiscordField{
			Name:  "Context",
			Value: truncate(ctx, 256),
		})
	}

	return DiscordEmbed{
		Title:  title,
		Color:  color,
		Fields: fields,
	}
}

// SendAnomalyUnraid dispatches an Unraid notification for an anomaly.
// The Unraid importance level is mapped from severity:
//   - "critical" → ImportanceAlert
//   - "warning"  → ImportanceWarning
//   - anything else → ImportanceNormal
//
// On non-Linux platforms (and Unraid hosts without the notify helper)
// the call is a no-op that logs and returns nil — same behaviour as Send.
func SendAnomalyUnraid(summary, details, severity string) error {
	imp := importanceForSeverity(severity)
	return Send("Vault", fmt.Sprintf("Anomaly detected: %s", summary), renderAnomalyDetails(details), imp)
}

// renderAnomalyDetails converts an anomaly's structured Details JSON into
// short, human-readable context lines for notifications, replacing the raw
// JSON blob that used to be shown to users. It only surfaces fields that add
// approachable context and deliberately omits purely statistical values
// (e.g. z_score, slope) that the plain-English Summary already conveys.
//
// Unrecognised or unparsable payloads yield an empty string so the caller
// simply omits the context rather than dumping raw JSON. The underlying
// Details JSON is left untouched for the API, MCP, and web UI that parse it.
func renderAnomalyDetails(details string) string {
	if strings.TrimSpace(details) == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(details), &m); err != nil {
		return ""
	}

	var lines []string
	// window_size counts the samples the detector looked back over — backup
	// runs for drift/reliability, but capacity health-check samples for the
	// capacity detector — so use the neutral "sample(s)" label rather than
	// "runs", which would misdescribe capacity anomalies.
	if n, ok := intField(m, "window_size"); ok && n > 0 {
		lines = append(lines, "Based on the last "+countLabel(n, "sample"))
	}
	if n, ok := intField(m, "streak"); ok && n > 0 {
		lines = append(lines, countLabel(n, "run")+" failed in a row")
	}
	if newest, ok := m["newest_status"].(string); ok {
		if prev, ok2 := m["previous_status"].(string); ok2 {
			lines = append(lines, fmt.Sprintf("Latest verification %s (previous run %s)", newest, prev))
		}
	}
	return strings.Join(lines, "\n")
}

// countLabel renders a count with a singular/plural unit, e.g. countLabel(1,
// "sample") → "1 sample" and countLabel(3, "run") → "3 runs".
func countLabel(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// intField reads a numeric JSON field as an int. JSON numbers decode to
// float64 through map[string]any, so only float64 is accepted; fractional,
// non-finite, or out-of-range values are rejected (ok=false) so malformed
// payloads never produce nonsense counts in user-facing notification text.
func intField(m map[string]any, key string) (int, bool) {
	f, ok := m[key].(float64)
	if !ok {
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < math.MinInt || f > math.MaxInt {
		return 0, false
	}
	return int(f), true
}

func importanceForSeverity(severity string) Importance {
	switch severity {
	case "critical":
		return ImportanceAlert
	case "warning":
		return ImportanceWarning
	default:
		return ImportanceNormal
	}
}

func scopeLabel(kind string) string {
	switch kind {
	case "job":
		return "Job"
	case "destination":
		return "Destination"
	default:
		return "Scope"
	}
}

// truncate caps s to maxLen runes, appending "…" when truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}

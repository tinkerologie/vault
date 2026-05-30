package notify

import "fmt"

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
	if p.Details != "" {
		fields = append(fields, DiscordField{
			Name:  "Details",
			Value: truncate(p.Details, 256),
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
	return Send("Vault", fmt.Sprintf("Anomaly detected: %s", summary), details, imp)
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

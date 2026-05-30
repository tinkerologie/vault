package notify

import (
	"testing"
)

// TestBuildAnomalyEmbed_ColourMapping verifies the Discord colour constants
// are assigned correctly by BuildAnomalyEmbed for each severity.
func TestBuildAnomalyEmbed_ColourMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		severity  string
		wantColor int
	}{
		{"critical", ColorAnomalyCritical},
		{"warning", ColorAnomalyWarning},
		{"resolved", ColorAnomalyResolved},
		{"info", ColorInfo},
		{"unknown", ColorInfo},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.severity, func(t *testing.T) {
			t.Parallel()
			embed := BuildAnomalyEmbed(AnomalyEmbedParams{
				Severity:  tc.severity,
				ScopeKind: "job",
				ScopeName: "my-job",
				Summary:   "disk usage high",
				Details:   "trajectory projects full in 3 days",
			})
			if embed.Color != tc.wantColor {
				t.Errorf("severity %q: color = 0x%06X, want 0x%06X",
					tc.severity, embed.Color, tc.wantColor)
			}
		})
	}
}

// TestBuildAnomalyEmbed_ColourValues asserts the exact hex constants so that
// a future refactor can't silently change them.
func TestBuildAnomalyEmbed_ColourValues(t *testing.T) {
	t.Parallel()

	if ColorAnomalyCritical != 0xDC2626 {
		t.Errorf("ColorAnomalyCritical = 0x%06X, want 0xDC2626", ColorAnomalyCritical)
	}
	if ColorAnomalyWarning != 0xF59E0B {
		t.Errorf("ColorAnomalyWarning = 0x%06X, want 0xF59E0B", ColorAnomalyWarning)
	}
	if ColorAnomalyResolved != 0x10B981 {
		t.Errorf("ColorAnomalyResolved = 0x%06X, want 0x10B981", ColorAnomalyResolved)
	}
}

// TestBuildAnomalyEmbed_RaiseVsEscalate verifies the embed title reflects the
// action correctly for initial raise vs escalation.
func TestBuildAnomalyEmbed_RaiseVsEscalate(t *testing.T) {
	t.Parallel()

	raise := BuildAnomalyEmbed(AnomalyEmbedParams{
		Severity: "critical",
		Summary:  "backup size exploded",
		IsUpdate: false,
	})
	if raise.Title == "" {
		t.Error("expected non-empty title for raise embed")
	}

	escalate := BuildAnomalyEmbed(AnomalyEmbedParams{
		Severity: "critical",
		Summary:  "backup size exploded",
		IsUpdate: true,
	})
	if escalate.Title == raise.Title {
		t.Errorf("escalation and raise titles are identical: %q", raise.Title)
	}
}

// TestBuildAnomalyEmbed_ScopeField verifies the scope field is only present
// when ScopeName is non-empty.
func TestBuildAnomalyEmbed_ScopeField(t *testing.T) {
	t.Parallel()

	withScope := BuildAnomalyEmbed(AnomalyEmbedParams{
		Severity:  "warning",
		ScopeKind: "job",
		ScopeName: "my-job",
		Summary:   "something odd",
	})
	foundScope := false
	for _, f := range withScope.Fields {
		if f.Value == "my-job" {
			foundScope = true
		}
	}
	if !foundScope {
		t.Error("expected scope field with value 'my-job' in embed fields")
	}

	noScope := BuildAnomalyEmbed(AnomalyEmbedParams{
		Severity: "warning",
		Summary:  "something odd",
	})
	for _, f := range noScope.Fields {
		if f.Value == "" {
			t.Errorf("embed field with empty value found: %+v", f)
		}
	}
}

// TestBuildAnomalyEmbed_DetailsTruncation verifies very long Details strings are
// capped so the Discord embed doesn't exceed field limits.
func TestBuildAnomalyEmbed_DetailsTruncation(t *testing.T) {
	t.Parallel()

	longDetails := make([]byte, 1000)
	for i := range longDetails {
		longDetails[i] = 'x'
	}

	embed := BuildAnomalyEmbed(AnomalyEmbedParams{
		Severity: "warning",
		Summary:  "test",
		Details:  string(longDetails),
	})

	for _, f := range embed.Fields {
		if f.Name == "Details" {
			if len([]rune(f.Value)) > 256 {
				t.Errorf("Details field length %d exceeds 256 runes", len([]rune(f.Value)))
			}
		}
	}
}

// TestSendAnomalyUnraid_NoError verifies that on non-Linux platforms the call
// is a no-op that returns nil (same as the underlying Send behaviour).
func TestSendAnomalyUnraid_NoError(t *testing.T) {
	t.Parallel()

	if err := SendAnomalyUnraid("size drift", "backup grew 3× median", "critical"); err != nil {
		t.Errorf("SendAnomalyUnraid() unexpected error: %v", err)
	}
	if err := SendAnomalyUnraid("reliability drop", "5 consecutive failures", "warning"); err != nil {
		t.Errorf("SendAnomalyUnraid() unexpected error: %v", err)
	}
	if err := SendAnomalyUnraid("info signal", "minor deviation", "info"); err != nil {
		t.Errorf("SendAnomalyUnraid() unexpected error: %v", err)
	}
}

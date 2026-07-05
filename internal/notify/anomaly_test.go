package notify

import (
	"strings"
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

// TestBuildAnomalyEmbed_ContextRendering verifies the embed surfaces friendly,
// labelled context derived from the Details JSON rather than dumping the raw
// blob (or leaking purely technical fields like z_score) at the user.
func TestBuildAnomalyEmbed_ContextRendering(t *testing.T) {
	t.Parallel()

	embed := BuildAnomalyEmbed(AnomalyEmbedParams{
		Severity: "warning",
		Summary:  "This backup grew to 20 GB, about 5× its usual 4 GB.",
		Details:  `{"z_score":11.06,"growth_factor":5,"window_size":10}`,
	})

	var ctx string
	for _, f := range embed.Fields {
		if f.Name == "Context" {
			ctx = f.Value
		}
		if f.Name == "Details" {
			t.Errorf("embed still exposes a raw Details field: %q", f.Value)
		}
	}
	if want := "Based on the last 10 samples"; ctx != want {
		t.Errorf("Context = %q, want %q", ctx, want)
	}
	if strings.Contains(ctx, "z_score") {
		t.Errorf("Context leaked a technical field: %q", ctx)
	}
	// The Context field must stay within Discord's per-field limit.
	if n := len([]rune(ctx)); n > 256 {
		t.Errorf("Context field length %d exceeds 256 runes", n)
	}
}

// TestTruncate verifies the field-length guard that keeps notification embed
// fields within Discord's 256-rune limit (regression guard for long values).
func TestTruncate(t *testing.T) {
	t.Parallel()

	if got := truncate("short", 256); got != "short" {
		t.Errorf("truncate short string = %q, want %q", got, "short")
	}

	long := strings.Repeat("x", 1000)
	got := truncate(long, 256)
	if n := len([]rune(got)); n > 256 {
		t.Errorf("truncate() length %d exceeds 256 runes", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate() should append an ellipsis when truncating, got %q", got)
	}
}

// TestBuildAnomalyEmbed_OmitsUnrenderableContext verifies that empty, non-JSON,
// or purely-technical Details payloads produce no Context field at all (the
// self-explanatory Summary stands on its own) rather than raw JSON.
func TestBuildAnomalyEmbed_OmitsUnrenderableContext(t *testing.T) {
	t.Parallel()

	for _, details := range []string{"", "not json", `{"pct_free":2.3}`} {
		embed := BuildAnomalyEmbed(AnomalyEmbedParams{
			Severity: "warning",
			Summary:  "test",
			Details:  details,
		})
		for _, f := range embed.Fields {
			if f.Name == "Context" {
				t.Errorf("details %q: unexpected Context field %q", details, f.Value)
			}
		}
	}
}

// TestRenderAnomalyDetails covers the readable-context rendering: neutral
// "sample(s)"/"run(s)" wording with singular/plural handling, verify-regression
// phrasing, and rejection of fractional/negative counts.
func TestRenderAnomalyDetails(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		details string
		want    string
	}{
		{"window plural", `{"z_score":8.2,"window_size":10}`, "Based on the last 10 samples"},
		{"window singular", `{"window_size":1}`, "Based on the last 1 sample"},
		{"streak plural", `{"streak":3}`, "3 runs failed in a row"},
		{"streak singular", `{"streak":1}`, "1 run failed in a row"},
		{"verify regression", `{"newest_status":"failed","previous_status":"passed"}`, "Latest verification failed (previous run passed)"},
		{"fractional window rejected", `{"window_size":10.9}`, ""},
		{"negative window rejected", `{"window_size":-4}`, ""},
		{"low-free has no context", `{"free_bytes":1000,"total_bytes":50000,"pct_free":2}`, ""},
		{"empty", "", ""},
		{"not json", "not json", ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := renderAnomalyDetails(c.details); got != c.want {
				t.Errorf("renderAnomalyDetails(%q) = %q, want %q", c.details, got, c.want)
			}
		})
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

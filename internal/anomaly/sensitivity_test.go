package anomaly

import (
	"testing"
	"time"
)

func TestSensitivityResolve(t *testing.T) {
	tests := []struct {
		name        string
		jobOverride string
		global      string
		want        Sensitivity
	}{
		// job override wins when valid and non-empty
		{"job=strict global=balanced", "strict", "balanced", SensStrict},
		{"job=balanced global=strict", "balanced", "strict", SensBalanced},
		{"job=permissive global=strict", "permissive", "strict", SensPermissive},
		{"job=strict global=permissive", "strict", "permissive", SensStrict},
		{"job=strict global=strict", "strict", "strict", SensStrict},
		{"job=balanced global=balanced", "balanced", "balanced", SensBalanced},
		{"job=permissive global=permissive", "permissive", "permissive", SensPermissive},

		// empty job override falls through to global
		{"job=empty global=strict", "", "strict", SensStrict},
		{"job=empty global=balanced", "", "balanced", SensBalanced},
		{"job=empty global=permissive", "", "permissive", SensPermissive},

		// both empty → SensBalanced
		{"job=empty global=empty", "", "", SensBalanced},

		// global invalid, job empty → SensBalanced (switch falls through)
		{"job=empty global=garbage", "", "garbage", SensBalanced},

		// job invalid (non-empty) → SensBalanced (global NOT consulted;
		// pick=garbage, switch fails → return SensBalanced).
		// NOTE: This means an invalid per-job override silently suppresses
		// a valid global setting. Flagged as a design concern below.
		{"job=garbage global=strict", "garbage", "strict", SensBalanced},
		{"job=garbage global=balanced", "garbage", "balanced", SensBalanced},
		{"job=garbage global=permissive", "garbage", "permissive", SensBalanced},
		{"job=garbage global=garbage", "garbage", "garbage", SensBalanced},

		// mixed valid/invalid edge cases
		{"job=strict global=empty", "strict", "", SensStrict},
		{"job=permissive global=empty", "permissive", "", SensPermissive},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.jobOverride, tc.global)
			if got != tc.want {
				t.Errorf("Resolve(%q, %q) = %q, want %q", tc.jobOverride, tc.global, got, tc.want)
			}
		})
	}
}

func TestSensitivityAccessors(t *testing.T) {
	tests := []struct {
		sens       Sensitivity
		wantK      float64
		wantStreak int
		wantDays   float64
	}{
		{SensStrict, 2.5, 1, 30},
		{SensBalanced, 3.5, 2, 14},
		{SensPermissive, 5.0, 3, 7},
	}

	for _, tc := range tests {
		t.Run(string(tc.sens), func(t *testing.T) {
			if got := tc.sens.K(); got != tc.wantK {
				t.Errorf("%s.K() = %v, want %v", tc.sens, got, tc.wantK)
			}
			if got := tc.sens.Streak(); got != tc.wantStreak {
				t.Errorf("%s.Streak() = %v, want %v", tc.sens, got, tc.wantStreak)
			}
			if got := tc.sens.WarnDays(); got != tc.wantDays {
				t.Errorf("%s.WarnDays() = %v, want %v", tc.sens, got, tc.wantDays)
			}
		})
	}
}

func TestFingerprintDeterministic(t *testing.T) {
	// Same inputs produce the same hash every time.
	fp1 := Fingerprint("duration_drift", ScopeJob, 42, "duration_seconds")
	fp2 := Fingerprint("duration_drift", ScopeJob, 42, "duration_seconds")
	if fp1 != fp2 {
		t.Errorf("Fingerprint is not deterministic: %q != %q", fp1, fp2)
	}

	// Different inputs produce different hashes.
	cases := []struct {
		detector, metric string
		scope            ScopeKind
		id               int64
	}{
		{"duration_drift", "duration_seconds", ScopeJob, 42},
		{"duration_drift", "duration_seconds", ScopeJob, 43},         // different ID
		{"duration_drift", "duration_seconds", ScopeDestination, 42}, // different scope
		{"duration_drift", "size_bytes", ScopeJob, 42},               // different metric
		{"size_drift", "duration_seconds", ScopeJob, 42},             // different detector
		// Boundary-shift pair: with length-prefixing, moving a '|' across
		// the detector/metric boundary must yield distinct fingerprints.
		{"a|b", "c", ScopeJob, 1},
		{"a", "b|c", ScopeJob, 1},
	}

	seen := make(map[string]struct{})
	for _, c := range cases {
		fp := Fingerprint(c.detector, c.scope, c.id, c.metric)
		if _, dup := seen[fp]; dup {
			t.Errorf("collision: Fingerprint(%q, %q, %d, %q) = %q already seen",
				c.detector, c.scope, c.id, c.metric, fp)
		}
		seen[fp] = struct{}{}
		if len(fp) != 64 {
			t.Errorf("expected 64-char hex, got %d chars: %q", len(fp), fp)
		}
	}
}

func TestAnomalyConvertersRoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	resolvedAt := now.Add(time.Hour)
	ackedAt := now.Add(2 * time.Hour)
	notifiedAt := now.Add(3 * time.Hour)
	expected := 123.45
	deviation := 6.78
	jobRunID := int64(99)

	orig := Anomaly{
		ID:             1,
		Fingerprint:    Fingerprint("test_detector", ScopeJob, 7, "bytes"),
		Detector:       "test_detector",
		Severity:       SeverityCritical,
		ScopeKind:      ScopeJob,
		ScopeID:        7,
		Metric:         "bytes",
		Observed:       999.0,
		Expected:       &expected,
		Deviation:      &deviation,
		JobRunID:       &jobRunID,
		Summary:        "backup size ballooned",
		Details:        "observed 999, expected 123.45",
		State:          StateAcknowledged,
		FirstSeenAt:    now,
		LastSeenAt:     now,
		ResolvedAt:     &resolvedAt,
		AcknowledgedAt: &ackedAt,
		AckAction:      AckMarkExpected,
		AckBy:          "admin",
		AckReason:      "known large backup",
		NotifiedAt:     &notifiedAt,
	}

	roundTripped := FromDB(ToDB(orig))

	if roundTripped.ID != orig.ID {
		t.Errorf("ID: got %d, want %d", roundTripped.ID, orig.ID)
	}
	if roundTripped.Fingerprint != orig.Fingerprint {
		t.Errorf("Fingerprint: got %q, want %q", roundTripped.Fingerprint, orig.Fingerprint)
	}
	if roundTripped.Severity != orig.Severity {
		t.Errorf("Severity: got %q, want %q", roundTripped.Severity, orig.Severity)
	}
	if roundTripped.ScopeKind != orig.ScopeKind {
		t.Errorf("ScopeKind: got %q, want %q", roundTripped.ScopeKind, orig.ScopeKind)
	}
	if roundTripped.State != orig.State {
		t.Errorf("State: got %q, want %q", roundTripped.State, orig.State)
	}
	if roundTripped.AckAction != orig.AckAction {
		t.Errorf("AckAction: got %q, want %q", roundTripped.AckAction, orig.AckAction)
	}
	if roundTripped.AckBy != orig.AckBy {
		t.Errorf("AckBy: got %q, want %q", roundTripped.AckBy, orig.AckBy)
	}
	if roundTripped.AckReason != orig.AckReason {
		t.Errorf("AckReason: got %q, want %q", roundTripped.AckReason, orig.AckReason)
	}

	// plain pass-through fields (no enum/pointer casting) — asserted so the
	// converter test stays authoritative if fields are added later.
	if roundTripped.Detector != orig.Detector {
		t.Errorf("Detector: got %q, want %q", roundTripped.Detector, orig.Detector)
	}
	if roundTripped.ScopeID != orig.ScopeID {
		t.Errorf("ScopeID: got %d, want %d", roundTripped.ScopeID, orig.ScopeID)
	}
	if roundTripped.Metric != orig.Metric {
		t.Errorf("Metric: got %q, want %q", roundTripped.Metric, orig.Metric)
	}
	if roundTripped.Observed != orig.Observed {
		t.Errorf("Observed: got %v, want %v", roundTripped.Observed, orig.Observed)
	}
	if roundTripped.Summary != orig.Summary {
		t.Errorf("Summary: got %q, want %q", roundTripped.Summary, orig.Summary)
	}
	if roundTripped.Details != orig.Details {
		t.Errorf("Details: got %q, want %q", roundTripped.Details, orig.Details)
	}
	if !roundTripped.FirstSeenAt.Equal(orig.FirstSeenAt) {
		t.Errorf("FirstSeenAt: got %v, want %v", roundTripped.FirstSeenAt, orig.FirstSeenAt)
	}
	if !roundTripped.LastSeenAt.Equal(orig.LastSeenAt) {
		t.Errorf("LastSeenAt: got %v, want %v", roundTripped.LastSeenAt, orig.LastSeenAt)
	}

	// pointer fields
	if roundTripped.Expected == nil || *roundTripped.Expected != expected {
		t.Errorf("Expected: got %v, want %v", roundTripped.Expected, expected)
	}
	if roundTripped.Deviation == nil || *roundTripped.Deviation != deviation {
		t.Errorf("Deviation: got %v, want %v", roundTripped.Deviation, deviation)
	}
	if roundTripped.JobRunID == nil || *roundTripped.JobRunID != jobRunID {
		t.Errorf("JobRunID: got %v, want %v", roundTripped.JobRunID, jobRunID)
	}
	if roundTripped.ResolvedAt == nil || !roundTripped.ResolvedAt.Equal(resolvedAt) {
		t.Errorf("ResolvedAt: got %v, want %v", roundTripped.ResolvedAt, resolvedAt)
	}
	if roundTripped.AcknowledgedAt == nil || !roundTripped.AcknowledgedAt.Equal(ackedAt) {
		t.Errorf("AcknowledgedAt: got %v, want %v", roundTripped.AcknowledgedAt, ackedAt)
	}
	if roundTripped.NotifiedAt == nil || !roundTripped.NotifiedAt.Equal(notifiedAt) {
		t.Errorf("NotifiedAt: got %v, want %v", roundTripped.NotifiedAt, notifiedAt)
	}

	// nil pointer fields survive round-trip as nil
	origNil := Anomaly{
		Fingerprint: "niltest",
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
	rtNil := FromDB(ToDB(origNil))
	if rtNil.Expected != nil {
		t.Errorf("Expected should be nil after round-trip, got %v", rtNil.Expected)
	}
	if rtNil.Deviation != nil {
		t.Errorf("Deviation should be nil after round-trip, got %v", rtNil.Deviation)
	}
	if rtNil.JobRunID != nil {
		t.Errorf("JobRunID should be nil after round-trip, got %v", rtNil.JobRunID)
	}
	if rtNil.ResolvedAt != nil {
		t.Errorf("ResolvedAt should be nil after round-trip, got %v", rtNil.ResolvedAt)
	}
	if rtNil.AcknowledgedAt != nil {
		t.Errorf("AcknowledgedAt should be nil after round-trip, got %v", rtNil.AcknowledgedAt)
	}
	if rtNil.NotifiedAt != nil {
		t.Errorf("NotifiedAt should be nil after round-trip, got %v", rtNil.NotifiedAt)
	}
}
